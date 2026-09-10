// Package auth: вход в RX через OIDC (Keycloak и любой другой провайдер).
// Authorization Code + PKCE, локальный колбэк на 127.0.0.1, кэш токенов в конфиге пользователя,
// обновление по refresh_token. Пароль пользователя rxmcp не видит: его вводят в браузере.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// OIDCConfig настройки провайдера.
type OIDCConfig struct {
	Issuer       string // https://sso.company.ru/realms/company
	ClientID     string
	ClientSecret string // пусто для публичного клиента
	Scope        string // по умолчанию openid profile offline_access
	RedirectPort int    // 0 = случайный
	CacheFile    string // где хранить токены; пусто = стандартный путь
	HTTP         *http.Client
}

type discovery struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	EndSessionEndpoint    string `json:"end_session_endpoint"`
}

// Tokens сохранённые токены.
type Tokens struct {
	Issuer       string    `json:"issuer"`
	ClientID     string    `json:"client_id"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	IDToken      string    `json:"id_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	Obtained     time.Time `json:"obtained"`
}

// DefaultCacheFile путь к файлу токенов: ~/.config/rxmcp/<host>.json (0600).
func DefaultCacheFile(issuer, clientID string) string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	u, _ := url.Parse(issuer)
	host := "oidc"
	if u != nil && u.Host != "" {
		host = strings.ReplaceAll(u.Host, ":", "_")
	}
	return filepath.Join(dir, "rxmcp", fmt.Sprintf("tokens-%s-%s.json", host, sanitize(clientID)))
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

func (c *OIDCConfig) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *OIDCConfig) cacheFile() string {
	if c.CacheFile != "" {
		return c.CacheFile
	}
	return DefaultCacheFile(c.Issuer, c.ClientID)
}

func (c *OIDCConfig) discover(ctx context.Context) (*discovery, error) {
	u := strings.TrimRight(c.Issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("провайдер OIDC недоступен: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("discovery %s вернул %d", u, resp.StatusCode)
	}
	var d discovery
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d); err != nil {
		return nil, err
	}
	if d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" {
		return nil, errors.New("в discovery нет authorization_endpoint или token_endpoint")
	}
	return &d, nil
}

func randB64(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Login открывает браузер, ждёт колбэк и сохраняет токены.
func (c *OIDCConfig) Login(ctx context.Context, out io.Writer) (*Tokens, error) {
	if c.Issuer == "" || c.ClientID == "" {
		return nil, errors.New("нужны RXMCP_OIDC_ISSUER и RXMCP_OIDC_CLIENT_ID")
	}
	d, err := c.discover(ctx)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", c.RedirectPort))
	if err != nil {
		return nil, fmt.Errorf("не удалось открыть порт для колбэка: %w", err)
	}
	defer ln.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)
	verifier := randB64(48)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := randB64(16)
	scope := c.Scope
	if scope == "" {
		scope = "openid profile offline_access"
	}
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {c.ClientID},
		"redirect_uri":          {redirect},
		"scope":                 {scope},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	authURL := d.AuthorizationEndpoint + "?" + q.Encode()

	type result struct {
		code string
		err  error
	}
	ch := make(chan result, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		qs := r.URL.Query()
		if qs.Get("state") != state {
			http.Error(w, "state mismatch", 400)
			ch <- result{err: errors.New("state не совпал, попробуйте ещё раз")}
			return
		}
		if e := qs.Get("error"); e != "" {
			http.Error(w, e+": "+qs.Get("error_description"), 400)
			ch <- result{err: fmt.Errorf("провайдер отказал: %s %s", e, qs.Get("error_description"))}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<!doctype html><meta charset=utf-8><title>rxmcp</title><body style='font-family:sans-serif;padding:2em'><h2>Вход выполнен</h2><p>Можно закрыть вкладку и вернуться в терминал.</p>")
		ch <- result{code: qs.Get("code")}
	})
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	fmt.Fprintf(out, "Откройте в браузере и войдите (если не открылось само):\n%s\n\n", authURL)
	openBrowser(authURL)

	var res result
	select {
	case res = <-ch:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(5 * time.Minute):
		return nil, errors.New("вход не завершён за 5 минут")
	}
	if res.err != nil {
		return nil, res.err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {res.code},
		"redirect_uri":  {redirect},
		"client_id":     {c.ClientID},
		"code_verifier": {verifier},
	}
	if c.ClientSecret != "" {
		form.Set("client_secret", c.ClientSecret)
	}
	t, err := c.exchange(ctx, d.TokenEndpoint, form)
	if err != nil {
		return nil, err
	}
	if err := c.save(t); err != nil {
		return nil, err
	}
	return t, nil
}

func (c *OIDCConfig) exchange(ctx context.Context, endpoint string, form url.Values) (*Tokens, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("token endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &tr)
	if resp.StatusCode != 200 || tr.AccessToken == "" {
		if tr.Error != "" {
			return nil, fmt.Errorf("провайдер не выдал токен: %s %s", tr.Error, tr.ErrorDesc)
		}
		return nil, fmt.Errorf("провайдер не выдал токен (HTTP %d)", resp.StatusCode)
	}
	exp := time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	if tr.ExpiresIn == 0 {
		exp = time.Now().Add(5 * time.Minute)
	}
	return &Tokens{Issuer: c.Issuer, ClientID: c.ClientID, AccessToken: tr.AccessToken, RefreshToken: tr.RefreshToken, IDToken: tr.IDToken, ExpiresAt: exp, Obtained: time.Now()}, nil
}

func (c *OIDCConfig) save(t *Tokens) error {
	f := c.cacheFile()
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(t, "", "  ")
	return os.WriteFile(f, data, 0o600)
}

// Load читает сохранённые токены.
func (c *OIDCConfig) Load() (*Tokens, error) {
	data, err := os.ReadFile(c.cacheFile())
	if err != nil {
		return nil, err
	}
	var t Tokens
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	if t.Issuer != c.Issuer || t.ClientID != c.ClientID {
		return nil, errors.New("сохранённые токены от другого провайдера или клиента")
	}
	return &t, nil
}

// Logout удаляет сохранённые токены.
func (c *OIDCConfig) Logout() error {
	err := os.Remove(c.cacheFile())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Source выдаёт актуальный access token, обновляя его по refresh_token.
type Source struct {
	cfg *OIDCConfig
	mu  sync.Mutex
	tok *Tokens
	d   *discovery
}

// NewSource создаёт источник токенов из кэша.
func NewSource(cfg *OIDCConfig) (*Source, error) {
	t, err := cfg.Load()
	if err != nil {
		return nil, fmt.Errorf("нет сохранённого входа: выполните `rxmcp login` (%v)", err)
	}
	return &Source{cfg: cfg, tok: t}, nil
}

// Token возвращает access token, при необходимости обновив его.
func (s *Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Until(s.tok.ExpiresAt) > 30*time.Second {
		return s.tok.AccessToken, nil
	}
	if s.tok.RefreshToken == "" {
		return "", errors.New("access token истёк, а refresh token не выдан: выполните `rxmcp login`")
	}
	if s.d == nil {
		d, err := s.cfg.discover(ctx)
		if err != nil {
			return "", err
		}
		s.d = d
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s.tok.RefreshToken}, "client_id": {s.cfg.ClientID}}
	if s.cfg.ClientSecret != "" {
		form.Set("client_secret", s.cfg.ClientSecret)
	}
	t, err := s.cfg.exchange(ctx, s.d.TokenEndpoint, form)
	if err != nil {
		return "", fmt.Errorf("обновление токена не удалось, выполните `rxmcp login`: %w", err)
	}
	if t.RefreshToken == "" {
		t.RefreshToken = s.tok.RefreshToken
	}
	s.tok = t
	_ = s.cfg.save(t)
	return t.AccessToken, nil
}

var openBrowser = func(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	_ = cmd.Start()
}

// Claims достаёт полезные поля из JWT без проверки подписи (только для показа пользователю).
func Claims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	return m
}
