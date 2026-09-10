package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Фейковый провайдер: discovery, authorize (сразу редиректит с кодом), token с проверкой PKCE.
func TestLoginAndRefresh(t *testing.T) {
	var challenge, issuedCode string
	refreshCalls := 0
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/x/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"authorization_endpoint": srv.URL + "/auth", "token_endpoint": srv.URL + "/token"})
		case "/auth":
			q := r.URL.Query()
			if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "rxmcp" || q.Get("response_type") != "code" {
				w.WriteHeader(400)
				return
			}
			challenge = q.Get("code_challenge")
			issuedCode = "code-123"
			http.Redirect(w, r, q.Get("redirect_uri")+"?code="+issuedCode+"&state="+url.QueryEscape(q.Get("state")), 302)
		case "/token":
			body, _ := io.ReadAll(r.Body)
			f, _ := url.ParseQuery(string(body))
			switch f.Get("grant_type") {
			case "authorization_code":
				sum := sha256.Sum256([]byte(f.Get("code_verifier")))
				if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge || f.Get("code") != issuedCode {
					w.WriteHeader(400)
					json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"access_token": "at1", "refresh_token": "rt1", "expires_in": 1})
			case "refresh_token":
				refreshCalls++
				if f.Get("refresh_token") != "rt1" {
					w.WriteHeader(400)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"access_token": "at2", "expires_in": 3600})
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	cfg := &OIDCConfig{Issuer: srv.URL + "/realms/x", ClientID: "rxmcp", CacheFile: filepath.Join(t.TempDir(), "tok.json")}
	// «Браузер»: идём по ссылке и следуем редиректу на локальный колбэк.
	openBrowser = func(u string) {
		go func() {
			resp, err := http.Get(u)
			if err == nil {
				resp.Body.Close()
			}
		}()
	}
	tok, err := cfg.Login(context.Background(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "at1" || tok.RefreshToken != "rt1" {
		t.Fatalf("tokens: %+v", tok)
	}
	src, err := NewSource(cfg)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // access token на 1 с истёк
	at, err := src.Token(context.Background())
	if err != nil || at != "at2" || refreshCalls != 1 {
		t.Fatalf("refresh: %v %q calls=%d", err, at, refreshCalls)
	}
	at, _ = src.Token(context.Background())
	if at != "at2" || refreshCalls != 1 {
		t.Error("второй вызов не должен обновлять токен")
	}
	saved, err := cfg.Load()
	if err != nil || saved.AccessToken != "at2" || saved.RefreshToken != "rt1" {
		t.Errorf("кэш: %v %+v", err, saved)
	}
	if err := cfg.Logout(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSource(cfg); err == nil || !strings.Contains(err.Error(), "rxmcp login") {
		t.Errorf("после logout нужен login: %v", err)
	}
}

func TestClaims(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"preferred_username":"ivanov"}`))
	if c := Claims("x." + payload + ".y"); c["preferred_username"] != "ivanov" {
		t.Errorf("claims: %v", c)
	}
	if Claims("bad") != nil {
		t.Error("мусор должен давать nil")
	}
}
