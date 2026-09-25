// Package odata: минимальный клиент сервиса интеграции Directum RX.
// Умеет ровно то, что нужно rxmcp: GET коллекций и сущностей с $-параметрами,
// POST действий модулей, скачивание тела версии. Никаких PATCH и DELETE.
package odata

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Client держит HTTP-клиент и способ аутентификации.
type Client struct {
	base     string
	auth     string
	login    string
	pass     string
	token    string
	cookie   string
	cookieFn func() (string, error)
	tokenFn  func(context.Context) (string, error)
	http     *http.Client
	MaxBody  int64
}

// Options параметры создания клиента.
type Options struct {
	BaseURL  string
	Auth     string
	Login    string
	Password string
	Token    string
	Cookie   string
	// CookieFunc отдаёт актуальную куку. Читается на каждом запросе, поэтому
	// обновление куки не требует перезапуска клиента. Имеет приоритет над Cookie.
	CookieFunc func() (string, error)
	// TokenFunc выдаёт актуальный bearer-токен (OIDC с обновлением). Имеет приоритет над Token.
	TokenFunc   func(context.Context) (string, error)
	Timeout     time.Duration
	InsecureTLS bool
	CAFile      string
}

// Error ошибка ответа RX с кодом.
type Error struct {
	Status int
	Method string
	Path   string
	Detail string
}

func (e *Error) Error() string {
	switch e.Status {
	case 401:
		return "RX отклонил учётку (401). Для basic проверьте логин и пароль (у логина должен быть тип «пароль»). Для cookie сессия истекла: войдите в RX в браузере и выполните `rxmcp login --paste`, перезапускать Claude не нужно. Для oidc: `rxmcp login`"
	case 403:
		return "нет прав (403): у этой учётки нет доступа к объекту или к сервису интеграции"
	case 404:
		if e.Method == "POST" {
			return "RX вернул 404 на действие " + e.Path + ": так он отвечает, когда не хватает обязательного параметра (например, срока или результата) или объект недоступен"
		}
		return "объект не найден или недоступен (404)"
	}
	if e.Detail != "" {
		return fmt.Sprintf("RX вернул %d на %s %s: %s", e.Status, e.Method, e.Path, e.Detail)
	}
	return fmt.Sprintf("RX вернул %d на %s %s", e.Status, e.Method, e.Path)
}

// New создаёт клиент.
func New(o Options) (*Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if o.InsecureTLS {
		tr.TLSClientConfig.InsecureSkipVerify = true //nolint:gosec // осознанный выбор пользователя
	}
	if o.CAFile != "" {
		pem, err := os.ReadFile(o.CAFile)
		if err != nil {
			return nil, fmt.Errorf("RXMCP_CA: %w", err)
		}
		pool, _ := x509.SystemCertPool()
		if pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("RXMCP_CA: в файле нет сертификатов PEM")
		}
		tr.TLSClientConfig.RootCAs = pool
	}
	if o.Timeout == 0 {
		o.Timeout = 30 * time.Second
	}
	return &Client{
		base:     strings.TrimRight(o.BaseURL, "/"),
		auth:     o.Auth,
		login:    o.Login,
		pass:     o.Password,
		token:    o.Token,
		cookie:   o.Cookie,
		cookieFn: o.CookieFunc,
		tokenFn:  o.TokenFunc,
		http:     &http.Client{Transport: tr, Timeout: o.Timeout},
		MaxBody:  64 << 20, // тела версий документов бывают большими (сканы, вложения)
	}, nil
}

// Base возвращает корень OData.
func (c *Client) Base() string { return c.base }

func (c *Client) setAuth(r *http.Request) error {
	switch c.auth {
	case "header":
		r.Header.Set("Username", c.login)
		r.Header.Set("Password", c.pass)
	case "bearer", "oidc":
		tok := c.token
		if c.tokenFn != nil {
			t, err := c.tokenFn(r.Context())
			if err != nil {
				return err
			}
			tok = t
		}
		r.Header.Set("Authorization", "Bearer "+tok)
	case "cookie":
		ck := c.cookie
		if c.cookieFn != nil {
			v, err := c.cookieFn()
			if err != nil {
				return fmt.Errorf("нет сохранённой куки: %w", err)
			}
			ck = v
		}
		r.Header.Set("Cookie", ck)
	default:
		r.SetBasicAuth(c.login, c.pass)
	}
	return nil
}

// Query параметры OData-запроса.
type Query struct {
	Filter  string
	Select  string
	Expand  string
	OrderBy string
	Top     int
	Skip    int
	Count   bool
}

func (q Query) values() url.Values {
	v := url.Values{}
	if q.Filter != "" {
		v.Set("$filter", q.Filter)
	}
	if q.Select != "" {
		v.Set("$select", q.Select)
	}
	if q.Expand != "" {
		v.Set("$expand", q.Expand)
	}
	if q.OrderBy != "" {
		v.Set("$orderby", q.OrderBy)
	}
	if q.Top > 0 {
		v.Set("$top", fmt.Sprint(q.Top))
	}
	if q.Skip > 0 {
		v.Set("$skip", fmt.Sprint(q.Skip))
	}
	if q.Count {
		v.Set("$count", "true")
	}
	return v
}

// Page ответ на запрос коллекции.
type Page struct {
	Count *int64            `json:"@odata.count"`
	Value []json.RawMessage `json:"value"`
	Next  string            `json:"@odata.nextLink"`
}

// List читает коллекцию: GET {base}/{set}?$…
func (c *Client) List(ctx context.Context, set string, q Query) (*Page, error) {
	body, err := c.get(ctx, set, q.values())
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		// RX отвечает 204 без тела, когда выборка пуста.
		zero := int64(0)
		return &Page{Count: &zero}, nil
	}
	var p Page
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("ответ RX не разобран: %w", err)
	}
	return &p, nil
}

// Get читает одну сущность: GET {base}/{set}({id})?$…
func (c *Client) Get(ctx context.Context, set string, id int64, q Query, out any) error {
	body, err := c.get(ctx, fmt.Sprintf("%s(%d)", set, id), q.values())
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return &Error{Status: 404, Method: "GET", Path: fmt.Sprintf("%s(%d)", set, id)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("ответ RX не разобран: %w", err)
	}
	return nil
}

// Raw делает GET по относительному пути и возвращает тело как есть.
func (c *Client) Raw(ctx context.Context, path string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/"+strings.TrimLeft(path, "/"), nil)
	if err != nil {
		return nil, "", err
	}
	if err := c.setAuth(req); err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "*/*")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", netErr(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.MaxBody+1))
	if err != nil {
		return nil, "", err
	}
	c.debug("GET", path, resp.StatusCode, len(data))
	if resp.StatusCode >= 300 {
		return nil, "", &Error{Status: resp.StatusCode, Method: "GET", Path: path, Detail: detail(data)}
	}
	if int64(len(data)) > c.MaxBody {
		return nil, "", fmt.Errorf("тело больше %d МБ, не читаем", c.MaxBody>>20)
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// Action вызывает действие модуля: POST {base}/{module}/{action} с JSON-параметрами.
// Возвращает тело ответа (может быть пустым).
func (c *Client) Action(ctx context.Context, module, action string, params map[string]any) ([]byte, error) {
	payload, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	path := module + "/" + action
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/"+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	if err := c.setAuth(req); err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, netErr(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	c.debug("POST", path, resp.StatusCode, len(data))
	if Debug && resp.StatusCode >= 300 {
		b := data
		if len(b) > 600 {
			b = b[:600]
		}
		fmt.Fprintf(os.Stderr, "rxmcp: тело ответа: %s\n", string(b))
	}
	if resp.StatusCode >= 300 {
		return nil, &Error{Status: resp.StatusCode, Method: "POST", Path: path, Detail: detail(data)}
	}
	return data, nil
}

func (c *Client) get(ctx context.Context, path string, v url.Values) ([]byte, error) {
	u := c.base + "/" + path
	if len(v) > 0 {
		u += "?" + v.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if err := c.setAuth(req); err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, netErr(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.MaxBody))
	if err != nil {
		return nil, err
	}
	c.debug("GET", u[len(c.base)+1:], resp.StatusCode, len(data))
	if resp.StatusCode >= 300 {
		return nil, &Error{Status: resp.StatusCode, Method: "GET", Path: path, Detail: detail(data)}
	}
	return data, nil
}

// Debug включает вывод запросов в stderr.
var Debug = os.Getenv("RXMCP_DEBUG") != ""

func (c *Client) debug(method, path string, status, n int) {
	if Debug {
		fmt.Fprintf(os.Stderr, "rxmcp: %s %s -> %d, %d байт\n", method, path, status, n)
	}
}

// detail вытаскивает сообщение из OData-ошибки, без стека и внутренностей.
func detail(data []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
		return cutStr(e.Error.Message, 300)
	}
	// RX часто отвечает голой JSON-строкой или текстом.
	var str string
	if json.Unmarshal(data, &str) == nil && str != "" {
		return cutStr(str, 300)
	}
	t := strings.TrimSpace(string(data))
	if t != "" && !strings.HasPrefix(t, "<") {
		return cutStr(t, 300)
	}
	return ""
}

func cutStr(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func netErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return fmt.Errorf("RX не ответил вовремя (%s)", ue.URL)
		}
		return fmt.Errorf("нет связи с RX: %v", ue.Err)
	}
	return err
}

// Quote экранирует строку для OData-литерала.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// Metadata запрашивает $metadata и возвращает размер (проверка связи).
func (c *Client) Metadata(ctx context.Context) (int, error) {
	data, _, err := c.Raw(ctx, "$metadata")
	if err != nil {
		return 0, err
	}
	return len(data), nil
}
