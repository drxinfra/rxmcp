package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/drxinfra/rxmcp/internal/config"
)

type bearerRT struct{ token string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// Сквозной HTTP-режим с персональными учётками: токен Keycloak → rxmcp → RX с тем же токеном,
// у каждого пользователя свой логин, чужие и испорченные токены не проходят.
func TestUsersHTTPMode(t *testing.T) {
	t.Setenv("RXMCP_HOME", t.TempDir())
	b64 := base64.RawURLEncoding
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	var idp *httptest.Server
	idp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/rx/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"issuer": idp.URL + "/realms/rx", "jwks_uri": idp.URL + "/realms/rx/certs"})
		case "/realms/rx/certs":
			json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kid": "k", "kty": "RSA",
				"n": b64.EncodeToString(key.N.Bytes()), "e": b64.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	sign := func(user string, roles ...string) string {
		h, _ := json.Marshal(map[string]any{"alg": "RS256", "kid": "k"})
		rs := []any{}
		for _, r := range roles {
			rs = append(rs, r)
		}
		c, _ := json.Marshal(map[string]any{"iss": idp.URL + "/realms/rx", "azp": "librechat", "preferred_username": user,
			"exp": time.Now().Add(5 * time.Minute).Unix(), "realm_access": map[string]any{"roles": rs}})
		in := b64.EncodeToString(h) + "." + b64.EncodeToString(c)
		sum := sha256.Sum256([]byte(in))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
		return in + "." + b64.EncodeToString(sig)
	}

	// RX: какой токен пришёл, тот пользователь и есть; запоминаем, что видели.
	var mu sync.Mutex
	seen := map[string]string{}
	tokens := map[string]string{}
	rxs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		user := tokens[tok]
		if user != "" {
			seen[user] = r.URL.Path
		}
		mu.Unlock()
		if user == "" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/IUsers") {
			f := r.URL.Query().Get("$filter")
			id := map[string]int{"ivanov": 7, "petrov": 8}[user]
			if !strings.Contains(f, "'"+user+"'") {
				io.WriteString(w, `{"value":[]}`)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"Id": id, "Name": strings.ToUpper(user[:1]) + user[1:], "Login": map[string]any{"LoginName": user}}}})
			return
		}
		w.WriteHeader(404)
	}))
	defer rxs.Close()

	t.Setenv("RXMCP_URL", rxs.URL)
	t.Setenv("RXMCP_AUTH", "passthrough")
	t.Setenv("RXMCP_HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("RXMCP_HTTP_AUTH", "oidc")
	t.Setenv("RXMCP_HTTP_OIDC_ISSUER", idp.URL+"/realms/rx")
	t.Setenv("RXMCP_HTTP_OIDC_AUDIENCE", "librechat")
	t.Setenv("RXMCP_HTTP_REQUIRE_ROLE", "rx-assistant")
	t.Setenv("RXMCP_UPDATE_CHECK", "0")
	cfg, err := config.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.URL = strings.TrimSuffix(rxs.URL, "/odata")
	h, err := usersHandler(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	whoami := func(token string) (string, error) {
		c := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
		cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp",
			HTTPClient: &http.Client{Transport: bearerRT{token}}}, nil)
		if err != nil {
			return "", err
		}
		defer cs.Close()
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "rx_whoami", Arguments: map[string]any{}})
		if err != nil {
			return "", err
		}
		return res.Content[0].(*mcp.TextContent).Text, nil
	}
	ti, tp := sign("ivanov", "rx-assistant"), sign("petrov", "rx-assistant")
	mu.Lock()
	tokens[ti], tokens[tp] = "ivanov", "petrov"
	mu.Unlock()
	for user, tok := range map[string]string{"Ivanov": ti, "Petrov": tp} {
		out, err := whoami(tok)
		if err != nil || !strings.Contains(out, user) {
			t.Errorf("%s: %q %v", user, out, err)
		}
	}
	if seen["ivanov"] == "" || seen["petrov"] == "" {
		t.Errorf("RX не получил токены пользователей: %v", seen)
	}
	if _, err := whoami(""); err == nil {
		t.Error("без токена пустили")
	}
	if _, err := whoami(sign("sidorov")); err == nil {
		t.Error("без роли rx-assistant пустили")
	}
	if _, err := whoami(ti[:len(ti)-4] + "AAAA"); err == nil {
		t.Error("с испорченной подписью пустили")
	}
}
