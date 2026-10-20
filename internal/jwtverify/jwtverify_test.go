package jwtverify

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type idp struct {
	srv  *httptest.Server
	rsa  *rsa.PrivateKey
	ec   *ecdsa.PrivateKey
	hits int
}

func newIdP(t *testing.T) *idp {
	t.Helper()
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	ek, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	p := &idp{rsa: rk, ec: ek}
	mux := http.NewServeMux()
	p.srv = httptest.NewServer(mux)
	mux.HandleFunc("/realms/rx/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"issuer": p.srv.URL + "/realms/rx", "jwks_uri": p.srv.URL + "/realms/rx/certs"})
	})
	mux.HandleFunc("/realms/rx/certs", func(w http.ResponseWriter, r *http.Request) {
		p.hits++
		ecPub, _ := ek.PublicKey.Bytes() // 0x04 || X || Y
		json.NewEncoder(w).Encode(map[string]any{"keys": []any{
			map[string]any{"kid": "r1", "kty": "RSA", "use": "sig", "n": b64.EncodeToString(rk.N.Bytes()), "e": b64.EncodeToString(big.NewInt(int64(rk.E)).Bytes())},
			map[string]any{"kid": "e1", "kty": "EC", "crv": "P-256", "x": b64.EncodeToString(ecPub[1:33]), "y": b64.EncodeToString(ecPub[33:])},
			map[string]any{"kid": "enc", "kty": "RSA", "use": "enc", "n": "AQAB", "e": "AQAB"},
		}})
	})
	t.Cleanup(p.srv.Close)
	return p
}

func (p *idp) sign(t *testing.T, alg, kid string, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(map[string]any{"alg": alg, "kid": kid, "typ": "JWT"})
	c, _ := json.Marshal(claims)
	in := b64.EncodeToString(h) + "." + b64.EncodeToString(c)
	sum := sha256.Sum256([]byte(in))
	var sig []byte
	switch alg {
	case "RS256":
		sig, _ = rsa.SignPKCS1v15(rand.Reader, p.rsa, crypto.SHA256, sum[:])
	case "ES256":
		r, s, _ := ecdsa.Sign(rand.Reader, p.ec, sum[:])
		sig = append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	}
	return in + "." + b64.EncodeToString(sig)
}

func TestVerify(t *testing.T) {
	p := newIdP(t)
	iss := p.srv.URL + "/realms/rx"
	v := &Verifier{Issuer: iss, Audience: "rxmcp"}
	ok := map[string]any{"iss": iss, "sub": "42", "preferred_username": "ivanov", "aud": []any{"rxmcp", "account"},
		"exp": float64(time.Now().Add(5 * time.Minute).Unix()), "realm_access": map[string]any{"roles": []any{"rx-assistant"}}}
	c, err := v.Verify(context.Background(), p.sign(t, "RS256", "r1", ok))
	if err != nil || c.Username != "ivanov" || len(c.Roles) != 1 || c.Roles[0] != "rx-assistant" {
		t.Fatalf("валидный RS256: %+v %v", c, err)
	}
	if _, err := v.Verify(context.Background(), p.sign(t, "ES256", "e1", ok)); err != nil {
		t.Fatalf("валидный ES256: %v", err)
	}
	with := func(k string, val any) map[string]any {
		m := map[string]any{}
		for a, b := range ok {
			m[a] = b
		}
		if val == nil {
			delete(m, k)
		} else {
			m[k] = val
		}
		return m
	}
	bad := map[string]string{
		"истёк":           p.sign(t, "RS256", "r1", with("exp", float64(time.Now().Add(-time.Hour).Unix()))),
		"чужой издатель":  p.sign(t, "RS256", "r1", with("iss", "https://evil/realms/rx")),
		"чужая аудитория": p.sign(t, "RS256", "r1", with("aud", "other")),
		"без логина":      p.sign(t, "RS256", "r1", with("preferred_username", nil)),
		"без срока":       p.sign(t, "RS256", "r1", with("exp", nil)),
		"неизвестный kid": p.sign(t, "RS256", "zz", ok),
		"ключ шифрования": p.sign(t, "RS256", "enc", ok),
	}
	for name, tok := range bad {
		if _, err := v.Verify(context.Background(), tok); err == nil {
			t.Errorf("%s: токен принят", name)
		}
	}
	good := p.sign(t, "RS256", "r1", ok)
	parts := strings.Split(good, ".")
	none := b64.EncodeToString([]byte(`{"alg":"none","kid":"r1"}`)) + "." + parts[1] + "."
	if _, err := v.Verify(context.Background(), none); err == nil {
		t.Error("alg none принят")
	}
	tampered := parts[0] + "." + b64.EncodeToString([]byte(`{"iss":"`+iss+`","preferred_username":"admin","aud":"rxmcp","exp":9999999999}`)) + "." + parts[2]
	if _, err := v.Verify(context.Background(), tampered); err == nil {
		t.Error("подменённое тело принято")
	}
	// azp вместо aud: так Keycloak помечает токен клиента, если аудитория не настроена.
	azp := with("aud", "account")
	azp["azp"] = "rxmcp"
	if _, err := v.Verify(context.Background(), p.sign(t, "RS256", "r1", azp)); err != nil {
		t.Errorf("azp=rxmcp: %v", err)
	}
	if p.hits > 2 {
		t.Errorf("ключи перечитаны %d раз: ограничение частоты не работает", p.hits)
	}
}
