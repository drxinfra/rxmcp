// Package weblogin получает куку RX так же, как веб-клиент, когда вход идёт через внешнего
// провайдера (Keycloak и другие OIDC). OIDC там выполняет сервер RX: браузер уходит на
// провайдера, тот по живой сессии сразу возвращает код формой на /Client/b1, RX выставляет
// external_identity, а вызов login.auto-logon-with-two-factor выдаёт sungero_client.
//
// rxmcp повторяет эту цепочку со своими куками провайдера. Пароль он не видит и client_id
// не нужен: клиент провайдера принадлежит серверу RX. Сессия провайдера живёт дольше куки RX
// (Keycloak по умолчанию часы, на корпоративных стендах дни), поэтому входить в браузере
// приходится только когда истекает она.
package weblogin

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// State куки провайдера для одного RX. Хранится в файле рядом с кукой RX, права 0600.
type State struct {
	// URL область кук провайдера, например https://sso.company.ru/realms/company/
	URL string `json:"url"`
	// Cookies заголовок Cookie для провайдера: имя=значение; …
	Cookies string `json:"cookies"`
	// ClientID идентификатор «вкладки» веб-клиента RX, держим постоянным
	ClientID string    `json:"client_id"`
	Updated  time.Time `json:"updated"`
}

// ErrNoSession провайдер просит войти заново: его сессия истекла.
var ErrNoSession = errors.New("сессия провайдера входа истекла: войдите в RX в браузере и выполните rxmcp login --sso")

// File путь к файлу с куками провайдера для адреса RX.
func File(dir, rxURL string) string {
	host := "rx"
	if u, e := url.Parse(rxURL); e == nil && u.Host != "" {
		host = strings.ReplaceAll(u.Host, ":", "_")
	}
	return filepath.Join(dir, "sso-"+host+".json")
}

// Load читает сохранённые куки провайдера.
func Load(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if s.URL == "" || s.Cookies == "" {
		return nil, fmt.Errorf("%s: нет кук провайдера", path)
	}
	return &s, nil
}

// Save пишет куки провайдера через временный файл: читать его могут сразу несколько процессов.
func Save(path string, s *State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sso-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Root корень веб-сервера RX по адресу сервиса интеграции: https://rx.company.ru
func Root(rxURL string) (string, error) {
	u, err := url.Parse(rxURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("адрес RX %q не разобран", rxURL)
	}
	return u.Scheme + "://" + u.Host, nil
}

// loginPath адрес, с которого веб-клиент уходит к провайдеру.
const loginPath = "/Client/login?redirect_uri=%2fClient%2f%23"

// Discover узнаёт область кук провайдера: куда RX отправляет на вход.
func Discover(ctx context.Context, tr http.RoundTripper, root string) (string, error) {
	cl := &http.Client{Transport: tr, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	jar, _ := cookiejar.New(nil)
	cl.Jar = jar
	next := root + loginPath
	// RX может сначала перекинуть на себя же (выставить служебные куки), к провайдеру не сразу.
	for range 5 {
		req, err := navigate(ctx, http.MethodGet, next, nil)
		if err != nil {
			return "", err
		}
		resp, err := cl.Do(req)
		if err != nil {
			return "", err
		}
		resp.Body.Close()
		loc, err := resp.Location()
		if err != nil {
			return "", fmt.Errorf("RX не отправил на вход к провайдеру (%d на %s): вход по SSO на этом стенде не настроен", resp.StatusCode, req.URL.Path)
		}
		if r, _ := url.Parse(root); loc.Host != r.Host {
			return scope(loc), nil
		}
		next = loc.String()
	}
	return "", errors.New("RX зациклил перенаправления на входе")
}

// scope область кук провайдера по адресу авторизации: для Keycloak это /realms/<realm>/.
func scope(auth *url.URL) string {
	p := auth.Path
	if i := strings.Index(p, "/protocol/"); i >= 0 {
		p = p[:i+1]
	} else {
		p = "/"
	}
	return auth.Scheme + "://" + auth.Host + p
}

// Renewer получает новую куку RX по сессии провайдера. Один на процесс: параллельные
// запросы после 401 ждут один вход, а не идут к провайдеру каждый сам.
type Renewer struct {
	Root      string
	Transport http.RoundTripper
	StateFile string
	// Save сохраняет полученную куку RX (заголовок Cookie целиком).
	Save func(string) error

	mu   sync.Mutex
	last time.Time
}

// Renew проходит вход и сохраняет новую куку RX и обновлённые куки провайдера.
func (r *Renewer) Renew(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Только что получили: второй 401 подряд значит, что дело не в куке.
	if time.Since(r.last) < 30*time.Second {
		return errors.New("кука получена только что, а RX всё равно отвечает 401")
	}
	st, err := Load(r.StateFile)
	if err != nil {
		return fmt.Errorf("нет сессии провайдера (rxmcp login --sso): %w", err)
	}
	cookie, err := Login(ctx, r.Transport, r.Root, st)
	if err != nil {
		return err
	}
	st.Updated = time.Now()
	if err := Save(r.StateFile, st); err != nil {
		return err
	}
	if err := r.Save(cookie); err != nil {
		return err
	}
	r.last = time.Now()
	return nil
}

// Login проходит цепочку входа веб-клиента и возвращает заголовок Cookie для RX.
// Куки провайдера в st обновляются: Keycloak перевыпускает их на каждом входе.
func Login(ctx context.Context, tr http.RoundTripper, root string, st *State) (string, error) {
	rootURL, err := url.Parse(root)
	if err != nil {
		return "", err
	}
	ssoURL, err := url.Parse(st.URL)
	if err != nil {
		return "", fmt.Errorf("адрес провайдера %q: %w", st.URL, err)
	}
	jar, _ := cookiejar.New(nil)
	jar.SetCookies(ssoURL, parseCookies(st.Cookies, ssoURL.Path))
	cl := &http.Client{Transport: tr, Jar: jar, Timeout: 60 * time.Second}

	// 1. RX отправляет к провайдеру, тот по живой сессии отдаёт форму с кодом.
	req, err := navigate(ctx, http.MethodGet, root+loginPath, nil)
	if err != nil {
		return "", err
	}
	resp, err := cl.Do(req)
	if err != nil {
		return "", err
	}
	page, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.Request.URL.Host != ssoURL.Host {
		return "", fmt.Errorf("RX не отправил к провайдеру %s, а остановился на %s (%d)", ssoURL.Host, resp.Request.URL.Path, resp.StatusCode)
	}
	action, fields := parseFormPost(string(page))
	if action == "" || fields.Get("code") == "" {
		// Провайдер показал страницу входа вместо формы с кодом.
		return "", ErrNoSession
	}
	a, err := url.Parse(action)
	if err != nil || a.Host != rootURL.Host {
		return "", fmt.Errorf("провайдер возвращает код не на RX, а на %q: не отправляю", action)
	}
	st.Cookies = joinCookies(jar.Cookies(ssoURL))

	// 2. Код уходит на RX, тот выставляет external_identity.
	req, err = navigate(ctx, http.MethodPost, action, strings.NewReader(fields.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", ssoURL.Scheme+"://"+ssoURL.Host)
	resp, err = cl.Do(req)
	if err != nil {
		return "", err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if !hasCookie(jar, rootURL, "external_identity") {
		return "", fmt.Errorf("RX не принял код провайдера (%d на %s)", resp.StatusCode, resp.Request.URL.Path)
	}

	// 3. Вход веб-клиента: RX выдаёт sungero_client.
	if st.ClientID == "" {
		st.ClientID = newUUID()
	}
	body, _ := json.Marshal(map[string]any{"UserUtcOffset": utcOffsetHours(), "CultureInfoName": "ru-RU"})
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, root+"/Client/api/login.auto-logon-with-two-factor", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", root)
	req.Header.Set("Referer", root+"/Client/")
	req.Header.Set("X-Client-Id", st.ClientID)
	req.Header.Set("X-Callcontext", "[]")
	req.Header.Set("X-Can-Use-Web-Agent", "false")
	resp, err = cl.Do(req)
	if err != nil {
		return "", err
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("вход веб-клиента RX: %d", resp.StatusCode)
	}
	var res struct {
		Challenge any `json:"Challenge"`
	}
	if json.Unmarshal(data, &res) == nil && res.Challenge != nil {
		return "", errors.New("RX просит второй фактор: такой вход rxmcp пройти не может")
	}
	for _, c := range jar.Cookies(rootURL) {
		if c.Name == "sungero_client" && c.Value != "" {
			return "sungero_client=" + c.Value, nil
		}
	}
	return "", errors.New("RX не выдал sungero_client после входа")
}

// navigate запрос как переход браузера по ссылке: RX отвечает перенаправлением на вход
// только на такие запросы, на фоновые он отдаёт страницу.
func navigate(ctx context.Context, method, u string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	return req, nil
}

var (
	reForm  = regexp.MustCompile(`(?is)<form[^>]*\baction\s*=\s*"([^"]*)"`)
	reInput = regexp.MustCompile(`(?is)<input\b[^>]*>`)
	reAttr  = regexp.MustCompile(`(?is)\b(name|value|type)\s*=\s*"([^"]*)"`)
)

// parseFormPost разбирает страницу response_mode=form_post: адрес формы и скрытые поля.
func parseFormPost(page string) (string, url.Values) {
	m := reForm.FindStringSubmatch(page)
	if m == nil {
		return "", nil
	}
	fields := url.Values{}
	for _, in := range reInput.FindAllString(page, -1) {
		var name, value, typ string
		for _, a := range reAttr.FindAllStringSubmatch(in, -1) {
			switch strings.ToLower(a[1]) {
			case "name":
				name = html.UnescapeString(a[2])
			case "value":
				value = html.UnescapeString(a[2])
			case "type":
				typ = strings.ToLower(a[2])
			}
		}
		if name != "" && typ == "hidden" {
			fields.Set(name, value)
		}
	}
	return html.UnescapeString(m[1]), fields
}

// parseCookies разбирает заголовок Cookie, скопированный из браузера.
func parseCookies(header, path string) []*http.Cookie {
	var out []*http.Cookie
	for _, p := range strings.Split(strings.TrimPrefix(strings.TrimSpace(header), "Cookie:"), ";") {
		name, val, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok || name == "" {
			continue
		}
		out = append(out, &http.Cookie{Name: name, Value: val, Path: path, Secure: true})
	}
	return out
}

// ParseProviderCookies проверяет, что в строке есть куки сессии провайдера.
func ParseProviderCookies(header string) (string, error) {
	cs := parseCookies(header, "/")
	if len(cs) == 0 {
		return "", errors.New("в строке нет кук")
	}
	for _, c := range cs {
		if c.Name == "KEYCLOAK_IDENTITY" || c.Name == "KEYCLOAK_IDENTITY_LEGACY" {
			return joinCookies(cs), nil
		}
	}
	names := make([]string, 0, len(cs))
	for _, c := range cs {
		names = append(names, c.Name)
	}
	return "", fmt.Errorf("нет KEYCLOAK_IDENTITY, есть только %s: скопируйте заголовок cookie запроса к провайдеру входа, а не к RX", strings.Join(names, ", "))
}

func joinCookies(cs []*http.Cookie) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

func hasCookie(jar http.CookieJar, u *url.URL, name string) bool {
	for _, c := range jar.Cookies(u) {
		if c.Name == name && c.Value != "" {
			return true
		}
	}
	return false
}

func utcOffsetHours() int {
	_, off := time.Now().Zone()
	return off / 3600
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
