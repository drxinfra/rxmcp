// Package jwtverify проверяет access token провайдера OIDC (Keycloak и других) по его
// открытым ключам: подпись, издателя, срок и аудиторию. Нужен HTTP-режиму с
// персональными учётками: чат-клиент передаёт токен вошедшего пользователя, rxmcp
// убеждается, что токен выдан нашим провайдером, и берёт из него логин.
//
// Только стандартная библиотека: RS256/384/512 и ES256/384. Ключи берутся из
// jwks_uri документа обнаружения и перечитываются, когда приходит неизвестный kid.
package jwtverify

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Claims поля токена, которые нужны серверу. Остальные лежат в Raw.
type Claims struct {
	Issuer   string
	Subject  string
	Username string // из настраиваемого поля, по умолчанию preferred_username
	Audience []string
	AZP      string
	Expires  time.Time
	Roles    []string // realm_access.roles у Keycloak
	Raw      map[string]any
}

// Verifier проверяет токены одного издателя.
type Verifier struct {
	Issuer    string        // ожидаемый iss, совпадает с адресом realm
	Audience  string        // если задан, должен быть в aud или равен azp
	UserClaim string        // поле с логином, по умолчанию preferred_username
	Skew      time.Duration // допуск часов, по умолчанию 30 с
	HTTP      *http.Client

	mu      sync.Mutex
	keys    map[string]crypto.PublicKey
	fetched time.Time
	jwksURI string
}

var b64 = base64.RawURLEncoding

// Verify разбирает и проверяет токен. Ошибка не раскрывает подробностей наружу:
// её текст пишется в журнал сервера, клиенту уходит 401.
func (v *Verifier) Verify(ctx context.Context, token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("не JWT")
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodePart(parts[0], &hdr); err != nil {
		return nil, fmt.Errorf("заголовок: %w", err)
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("подпись не base64url")
	}
	key, err := v.key(ctx, hdr.Kid)
	if err != nil {
		return nil, err
	}
	if err := checkSig(hdr.Alg, key, []byte(parts[0]+"."+parts[1]), sig); err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := decodePart(parts[1], &raw); err != nil {
		return nil, fmt.Errorf("тело: %w", err)
	}
	c := &Claims{Raw: raw}
	c.Issuer, _ = raw["iss"].(string)
	c.Subject, _ = raw["sub"].(string)
	c.AZP, _ = raw["azp"].(string)
	claim := v.UserClaim
	if claim == "" {
		claim = "preferred_username"
	}
	c.Username, _ = raw[claim].(string)
	switch a := raw["aud"].(type) {
	case string:
		c.Audience = []string{a}
	case []any:
		for _, x := range a {
			if s, ok := x.(string); ok {
				c.Audience = append(c.Audience, s)
			}
		}
	}
	if ra, ok := raw["realm_access"].(map[string]any); ok {
		if rs, ok := ra["roles"].([]any); ok {
			for _, r := range rs {
				if s, ok := r.(string); ok {
					c.Roles = append(c.Roles, s)
				}
			}
		}
	}
	skew := v.Skew
	if skew == 0 {
		skew = 30 * time.Second
	}
	now := time.Now()
	exp, ok := raw["exp"].(float64)
	if !ok {
		return nil, errors.New("в токене нет срока действия (exp)")
	}
	c.Expires = time.Unix(int64(exp), 0)
	if now.After(c.Expires.Add(skew)) {
		return nil, errors.New("срок токена истёк")
	}
	if nbf, ok := raw["nbf"].(float64); ok && now.Add(skew).Before(time.Unix(int64(nbf), 0)) {
		return nil, errors.New("токен ещё не действует (nbf)")
	}
	if strings.TrimRight(c.Issuer, "/") != strings.TrimRight(v.Issuer, "/") {
		return nil, fmt.Errorf("чужой издатель %q", c.Issuer)
	}
	if v.Audience != "" && c.AZP != v.Audience && !contains(c.Audience, v.Audience) {
		return nil, fmt.Errorf("токен выдан не для %q", v.Audience)
	}
	if c.Username == "" {
		return nil, fmt.Errorf("в токене нет поля %q с логином", claim)
	}
	return c, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func decodePart(p string, v any) error {
	b, err := b64.DecodeString(p)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func checkSig(alg string, key crypto.PublicKey, signed, sig []byte) error {
	var h hash.Hash
	var ch crypto.Hash
	switch alg {
	case "RS256", "ES256":
		h, ch = sha256.New(), crypto.SHA256
	case "RS384", "ES384":
		h, ch = sha512.New384(), crypto.SHA384
	case "RS512":
		h, ch = sha512.New(), crypto.SHA512
	default:
		// none, HS* и прочее не принимаем: подпись должна проверяться открытым ключом.
		return fmt.Errorf("алгоритм %q не поддерживается", alg)
	}
	h.Write(signed)
	sum := h.Sum(nil)
	switch k := key.(type) {
	case *rsa.PublicKey:
		if !strings.HasPrefix(alg, "RS") {
			return errors.New("алгоритм не совпадает с типом ключа")
		}
		if rsa.VerifyPKCS1v15(k, ch, sum, sig) != nil {
			return errors.New("подпись не сходится")
		}
	case *ecdsa.PublicKey:
		if !strings.HasPrefix(alg, "ES") {
			return errors.New("алгоритм не совпадает с типом ключа")
		}
		n := len(sig) / 2
		if len(sig)%2 != 0 || !ecdsa.Verify(k, sum, new(big.Int).SetBytes(sig[:n]), new(big.Int).SetBytes(sig[n:])) {
			return errors.New("подпись не сходится")
		}
	default:
		return errors.New("неизвестный тип ключа")
	}
	return nil
}

// key ключ по kid; при неизвестном kid ключи перечитываются, но не чаще раза в 30 секунд,
// чтобы поток мусорных токенов не превращался в поток запросов к провайдеру.
func (v *Verifier) key(ctx context.Context, kid string) (crypto.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if k, ok := v.keys[kid]; ok && time.Since(v.fetched) < 24*time.Hour {
		return k, nil
	}
	if time.Since(v.fetched) < 30*time.Second && v.keys != nil {
		if k, ok := v.keys[kid]; ok {
			return k, nil
		}
		return nil, fmt.Errorf("неизвестный ключ %q", kid)
	}
	if err := v.refresh(ctx); err != nil {
		return nil, err
	}
	if k, ok := v.keys[kid]; ok {
		return k, nil
	}
	if kid == "" && len(v.keys) == 1 {
		for _, k := range v.keys {
			return k, nil
		}
	}
	return nil, fmt.Errorf("неизвестный ключ %q", kid)
}

func (v *Verifier) client() *http.Client {
	if v.HTTP != nil {
		return v.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (v *Verifier) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := v.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

func (v *Verifier) refresh(ctx context.Context) error {
	if v.jwksURI == "" {
		var d struct {
			Issuer  string `json:"issuer"`
			JWKSURI string `json:"jwks_uri"`
		}
		if err := v.getJSON(ctx, strings.TrimRight(v.Issuer, "/")+"/.well-known/openid-configuration", &d); err != nil {
			return fmt.Errorf("документ обнаружения провайдера: %w", err)
		}
		if strings.TrimRight(d.Issuer, "/") != strings.TrimRight(v.Issuer, "/") {
			return fmt.Errorf("провайдер называет себя %q, ожидали %q", d.Issuer, v.Issuer)
		}
		v.jwksURI = d.JWKSURI
	}
	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err := v.getJSON(ctx, v.jwksURI, &set); err != nil {
		return fmt.Errorf("ключи провайдера: %w", err)
	}
	keys := map[string]crypto.PublicKey{}
	for _, k := range set.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		switch k.Kty {
		case "RSA":
			n, e1 := b64.DecodeString(k.N)
			e, e2 := b64.DecodeString(k.E)
			if e1 != nil || e2 != nil {
				continue
			}
			keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		case "EC":
			var curve elliptic.Curve
			switch k.Crv {
			case "P-256":
				curve = elliptic.P256()
			case "P-384":
				curve = elliptic.P384()
			default:
				continue
			}
			x, e1 := b64.DecodeString(k.X)
			y, e2 := b64.DecodeString(k.Y)
			if e1 != nil || e2 != nil {
				continue
			}
			size := (curve.Params().BitSize + 7) / 8
			if len(x) > size || len(y) > size {
				continue
			}
			buf := make([]byte, 1+2*size)
			buf[0] = 4
			copy(buf[1+size-len(x):1+size], x)
			copy(buf[1+2*size-len(y):], y)
			pk, err := ecdsa.ParseUncompressedPublicKey(curve, buf)
			if err != nil {
				continue
			}
			keys[k.Kid] = pk
		}
	}
	if len(keys) == 0 {
		return errors.New("у провайдера нет ключей подписи")
	}
	v.keys, v.fetched = keys, time.Now()
	return nil
}
