package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
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

// usersEnv HTTP-режим с персональными учётками на фейках: провайдер OIDC выдаёт подписанные
// токены, RX узнаёт пользователя по токену и запоминает, что тот запрашивал.
type usersEnv struct {
	srv  *httptest.Server
	sign func(user string, roles ...string) string

	mu        sync.Mutex
	seen      map[string]string // пользователь → последний путь в RX
	tokens    map[string]string // токен → пользователь
	completed map[string]int    // пользователь → сколько раз выполнил задание #5
}

func (e *usersEnv) token(user string) string {
	tok := e.sign(user, "rx-assistant")
	e.mu.Lock()
	e.tokens[tok] = user
	e.mu.Unlock()
	return tok
}

func (e *usersEnv) client(token string, opts *mcp.ClientOptions) (*mcp.ClientSession, error) {
	return mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, opts).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: e.srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerRT{token}}}, nil)
}

func newUsersEnv(t *testing.T, log *slog.Logger) *usersEnv {
	t.Helper()
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
	t.Cleanup(idp.Close)
	e := &usersEnv{seen: map[string]string{}, tokens: map[string]string{}, completed: map[string]int{}}
	e.sign = func(user string, roles ...string) string {
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
	rxs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		e.mu.Lock()
		user := e.tokens[tok]
		if user != "" {
			e.seen[user] = r.URL.Path
		}
		e.mu.Unlock()
		if user == "" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/IUsers"):
			f := r.URL.Query().Get("$filter")
			id := map[string]int{"ivanov": 7, "petrov": 8}[user]
			if !strings.Contains(f, "'"+user+"'") {
				io.WriteString(w, `{"value":[]}`)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"Id": id, "Name": strings.ToUpper(user[:1]) + user[1:], "Login": map[string]any{"LoginName": user}}}})
		case strings.HasSuffix(r.URL.Path, "/IAssignments(5)"):
			json.NewEncoder(w).Encode(map[string]any{"@odata.type": "#X.Workflow.ISimpleAssignmentDto", "Id": 5, "Subject": "Проверить", "Status": "InProcess"})
		case strings.HasSuffix(r.URL.Path, "/Docflow/CompleteAssignment"):
			e.mu.Lock()
			e.completed[user]++
			e.mu.Unlock()
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(rxs.Close)

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
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	h, err := usersHandler(cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	e.srv = httptest.NewServer(h)
	t.Cleanup(e.srv.Close)
	return e
}

// Сквозной HTTP-режим с персональными учётками: токен Keycloak → rxmcp → RX с тем же токеном,
// у каждого пользователя свой логин, чужие и испорченные токены не проходят.
func TestUsersHTTPMode(t *testing.T) {
	e := newUsersEnv(t, nil)
	whoami := func(token string) (string, error) {
		cs, err := e.client(token, nil)
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
	ti, tp := e.token("ivanov"), e.token("petrov")
	for user, tok := range map[string]string{"Ivanov": ti, "Petrov": tp} {
		out, err := whoami(tok)
		if err != nil || !strings.Contains(out, user) {
			t.Errorf("%s: %q %v", user, out, err)
		}
	}
	if e.seen["ivanov"] == "" || e.seen["petrov"] == "" {
		t.Errorf("RX не получил токены пользователей: %v", e.seen)
	}
	if _, err := whoami(""); err == nil {
		t.Error("без токена пустили")
	}
	if _, err := whoami(e.sign("sidorov")); err == nil {
		t.Error("без роли rx-assistant пустили")
	}
	if _, err := whoami(ti[:len(ti)-4] + "AAAA"); err == nil {
		t.Error("с испорченной подписью пустили")
	}
}

// Подтверждение записи в HTTP-режиме с персональными учётками (протокол 2026-07-28): сервер
// собирается на каждый запрос, ответ на форму приходит уже в другой экземпляр и принимается
// только от того же пользователя и только один раз.
func TestUsersHTTPConfirm(t *testing.T) {
	t.Setenv("RXMCP_ALLOW_WRITE", "1")
	var logBuf strings.Builder
	var logMu sync.Mutex
	e := newUsersEnv(t, slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		logMu.Lock()
		defer logMu.Unlock()
		return logBuf.Write(p)
	}), nil)))
	manual := &mcp.ClientOptions{
		ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			return nil, errors.New("форму показывает тест")
		},
		MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
	}
	call := func(token string, answers mcp.InputResponseMap, state string) *mcp.CallToolResult {
		t.Helper()
		cs, err := e.client(token, manual)
		if err != nil {
			t.Fatal(err)
		}
		defer cs.Close()
		if v := cs.InitializeResult().ProtocolVersion; v != "2026-07-28" {
			t.Fatalf("протокол %s", v)
		}
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "rx_complete_assignment",
			Arguments: map[string]any{"id": 5}, InputResponses: answers, RequestState: state})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	text := func(res *mcp.CallToolResult) string {
		if len(res.Content) == 0 {
			return ""
		}
		return res.Content[0].(*mcp.TextContent).Text
	}
	accept := mcp.InputResponseMap{"confirm": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}}
	ti, tp := e.token("ivanov"), e.token("petrov")

	first := call(ti, nil, "")
	if p, _ := first.InputRequests["confirm"].(*mcp.ElicitParams); !first.NeedsInput() || p == nil || p.Message != "Завершить простое задание #5 «Проверить»?" {
		t.Fatalf("первый заход не форма: %+v", first)
	}
	// Петров предъявляет requestState Иванова: для него это новый вызов, снова форма.
	if res := call(tp, accept, first.RequestState); !res.NeedsInput() {
		t.Errorf("чужой ответ принят: %q", text(res))
	}
	e.mu.Lock()
	n := len(e.completed)
	e.mu.Unlock()
	if n != 0 {
		t.Fatalf("без согласия в RX ушло выполнение: %v", e.completed)
	}
	if res := call(ti, accept, first.RequestState); res.IsError || text(res) != "Задание #5 (простое задание) завершено с результатом Complete." {
		t.Errorf("ответ Иванова: %q", text(res))
	}
	if res := call(ti, accept, first.RequestState); !res.IsError {
		t.Errorf("повтор того же ответа: %q", text(res))
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.completed["ivanov"] != 1 || e.completed["petrov"] != 0 {
		t.Errorf("выполнения в RX: %v", e.completed)
	}
	logMu.Lock()
	defer logMu.Unlock()
	if !strings.Contains(logBuf.String(), "user=ivanov tool=rx_complete_assignment params=id error=false") ||
		strings.Count(logBuf.String(), "awaiting=confirmation") != 2 {
		t.Errorf("журнал вызовов:\n%s", logBuf.String())
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
