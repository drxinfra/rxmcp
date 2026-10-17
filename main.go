// rxmcp: MCP-сервер для Directum RX.
package main

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/drxinfra/rxmcp/internal/auth"
	"github.com/drxinfra/rxmcp/internal/config"
	"github.com/drxinfra/rxmcp/internal/help"
	"github.com/drxinfra/rxmcp/internal/odata"
	"github.com/drxinfra/rxmcp/internal/rx"
	"github.com/drxinfra/rxmcp/internal/server"
	"github.com/drxinfra/rxmcp/internal/weblogin"
)

var version = "dev"

const usage = `rxmcp %s: MCP-сервер для Directum RX (drxinfra.ru/rxmcp)

Начало работы:
  rxmcp setup           спросит адрес RX, логин и способ входа, проверит связь
                        и сам прописывает сервер в Claude Code, Claude Desktop, Cursor
  rxmcp login           обновить вход (кука из браузера или вход через провайдера)
  rxmcp login --paste   то же, но куку берёт из буфера обмена (в WSL из буфера Windows)
  rxmcp login --stdin   куку берёт со стандартного ввода: rxmcp login --stdin < cookie.txt
  rxmcp login --sso     вход через сессию провайдера (Keycloak): rxmcp сам берёт новую куку RX,
                        когда старая истекает, пока жива сессия провайдера
  rxmcp check           проверить подключение к RX одним чтением и показать, что работает
  rxmcp docs index      скачать справку со своего стенда, чтобы помощник отвечал по ней
                        (если не выполнить, справка скачается сама при первом вопросе)

Остальные команды:
  rxmcp                 запустить сервер по stdio (так его вызывает MCP-клиент)
  rxmcp serve --http    запустить по HTTP (Streamable HTTP) на RXMCP_HTTP_ADDR, нужен RXMCP_HTTP_SECRET
  rxmcp config          показать профиль; config set КЛЮЧ=значение, config unset КЛЮЧ, config path
  rxmcp logout          удалить сохранённые куку и токены
  rxmcp install         напечатать фрагменты конфигурации для клиентов; --apply прописать сразу
  rxmcp query SET [$k=v …]   один GET к OData для отладки, например: rxmcp query IAssignments '$top=1'
  rxmcp call Module/Action '{json}'   один POST действия для отладки (меняет данные, если действие пишущее)
  rxmcp docs search СЛОВА   поиск по скачанной справке; docs topic ФАЙЛ, docs status
  rxmcp update          поставить последнюю версию вместо текущей (архив сверяется с SHA256SUMS)
  rxmcp version         версия; заодно скажет, если вышла новая

Настройки лежат в профиле (rxmcp config path), права 0600. Переменные окружения
имеют приоритет над профилем — так удобно в контейнере и в CI. Имена одинаковые:

  RXMCP_URL           адрес сервиса интеграции, например https://rx.company.ru/Integration
  RXMCP_AUTH          basic (логин и пароль) | cookie | oidc | bearer | header
  RXMCP_LOGIN         логин пользователя RX (нужен всегда: по нему ищутся ваши задания)
  RXMCP_PASSWORD      пароль (RXMCP_AUTH=basic)
  RXMCP_COOKIE        заголовок Cookie из браузера; обычно не нужен, куку держит rxmcp login
  RXMCP_TOKEN         токен для RXMCP_AUTH=bearer
  RXMCP_OIDC_ISSUER   адрес realm, например https://sso.company.ru/realms/company (RXMCP_AUTH=oidc)
  RXMCP_OIDC_CLIENT_ID, RXMCP_OIDC_CLIENT_SECRET, RXMCP_OIDC_SCOPE, RXMCP_OIDC_PORT
  RXMCP_OIDC_FLOW     code (по умолчанию, вход в браузере) | device (код на экране, для серверов)
  RXMCP_USER_ID       Id пользователя RX, если его нельзя вычислить по логину
  RXMCP_ALLOW_WRITE   1 = включить инструменты записи (карточки, колонки, задачи, выполнение заданий)
  RXMCP_CONFIRM       0 = не спрашивать подтверждение формой перед записью (если клиент её не показывает)
  RXMCP_INSECURE_TLS  1 = не проверять сертификат RX (только для тестовых стендов)
  RXMCP_CA            файл PEM с корневым сертификатом вашего УЦ
  RXMCP_TIMEOUT       таймаут запроса к RX, по умолчанию 30s
  RXMCP_MAX_TEXT      лимит текста документа в символах, по умолчанию 20000
  RXMCP_TZ            часовой пояс для дат, например Europe/Moscow (по умолчанию системный)
  RXMCP_HOME          каталог настроек, по умолчанию ~/.config/rxmcp
  RXMCP_HELP_URL      каталог справки, если он не рядом с веб-клиентом (по умолчанию <адрес RX>/Client/WebHelp/ru-RU)
  RXMCP_HTTP_ADDR     адрес для serve --http, например 127.0.0.1:8765
  RXMCP_HTTP_SECRET   общий секрет для HTTP-режима, клиент шлёт Authorization: Bearer <секрет>
  RXMCP_UPDATE_CHECK  0 = не узнавать о новых версиях (по умолчанию раз в сутки спрашивает
                      drxinfra.ru/dl/rxmcp/latest.txt, при недоступности GitHub; больше ничего не шлёт)
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "rxmcp:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}
	switch cmd {
	case "version", "-v", "--version":
		fmt.Println("rxmcp", version)
		if v := availableUpdate(3 * time.Second); v != "" {
			fmt.Printf("Вышла %s: rxmcp update\n", v)
		}
		return nil
	case "update":
		return selfUpdate()
	case "help", "-h", "--help":
		fmt.Printf(usage, version)
		return nil
	case "setup":
		return setup(args)
	case "config":
		return configCmd(args)
	case "install":
		for _, a := range args {
			if a == "--apply" {
				// Прописать в клиенты, ничего не спрашивая: удобно, когда профиль уже есть.
				return registerClients("rx", true, nil)
			}
			return fmt.Errorf("install: неизвестный аргумент %q (есть --apply)", a)
		}
		return install()
	case "docs":
		return docs(args)
	case "check":
		return check()
	case "query":
		return query(args)
	case "call":
		return call(args)
	case "login":
		src, cookieFlag := "", false
		for _, a := range args {
			switch a {
			case "--cookie":
				cookieFlag = true
			case "--paste":
				src = "paste"
			case "--stdin":
				src = "stdin"
			case "--sso":
				return loginSSO()
			default:
				return fmt.Errorf("login: неизвестный аргумент %q (есть --paste, --stdin, --sso)", a)
			}
		}
		if !cookieFlag && src == "" {
			// Без флагов делаем то, что настроено: спорить с человеком не о чем.
			if cfg, err := config.FromEnv(); err == nil && cfg.Auth == "oidc" {
				return login()
			}
		}
		return loginCookie(src)
	case "logout":
		cfg, err := config.FromEnv()
		if err != nil {
			return err
		}
		if err := auth.ForgetCookie(cfg.URL); err != nil {
			return err
		}
		if err := os.Remove(weblogin.File(auth.Dir(), cfg.URL)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if cfg.OIDCIssuer != "" {
			return oidcConfig(cfg).Logout()
		}
		fmt.Println("Кука удалена. Вернуть: rxmcp login")
		return nil
	case "serve":
		httpMode := false
		for _, a := range args {
			if a == "--http" {
				httpMode = true
			}
		}
		return serve(httpMode)
	}
	return fmt.Errorf("неизвестная команда %q; rxmcp help", cmd)
}

func logger() *slog.Logger {
	lvl := slog.LevelInfo
	if os.Getenv("RXMCP_DEBUG") != "" {
		lvl = slog.LevelDebug
	}
	// Только stderr: stdout занят протоколом.
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

func build() (*config.Config, *rx.Service, error) {
	cfg, err := config.FromEnv()
	if err != nil {
		return nil, nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}
	cl, err := newClient(cfg)
	if err != nil {
		return nil, nil, err
	}
	f := rx.Formatter{Loc: cfg.Location()}
	svc := rx.New(cl, cfg.Login, cfg.UserID, cfg.PageSize, cfg.MaxPageSize, f)
	return cfg, svc, nil
}

func oidcConfig(cfg *config.Config) *auth.OIDCConfig {
	return &auth.OIDCConfig{Issuer: cfg.OIDCIssuer, ClientID: cfg.OIDCClientID, ClientSecret: cfg.OIDCClientSecret, Scope: cfg.OIDCScope, RedirectPort: cfg.OIDCPort}
}

func newClient(cfg *config.Config) (*odata.Client, error) {
	o := odata.Options{
		BaseURL: cfg.ODataURL(), Auth: cfg.Auth, Login: cfg.Login, Password: cfg.Password, Token: cfg.Token, Cookie: cfg.Cookie,
		Timeout: cfg.Timeout, InsecureTLS: cfg.InsecureTLS, CAFile: cfg.CAFile,
	}
	if cfg.Auth == "cookie" && cfg.Cookie == "" {
		// куку читаем из файла на каждом запросе: обновил файл — сервер подхватил,
		// перезапускать Claude не нужно
		u := cfg.URL
		o.CookieFunc = func() (string, error) { return auth.LoadCookie(u) }
		o.CookieSave = func(c string) error { return auth.SaveCookie(u, c) }
		// Есть сессия провайдера (rxmcp login --sso): истёкшую куку rxmcp перевыпустит сам.
		if sf := weblogin.File(auth.Dir(), u); fileExists(sf) {
			root, err := weblogin.Root(u)
			if err != nil {
				return nil, err
			}
			tr, err := odata.NewTransport(cfg.InsecureTLS, cfg.CAFile)
			if err != nil {
				return nil, err
			}
			r := &weblogin.Renewer{Root: root, Transport: tr, StateFile: sf, Save: o.CookieSave}
			o.CookieRenew = r.Renew
		}
	}
	if cfg.Auth == "oidc" {
		src, err := auth.NewSource(oidcConfig(cfg))
		if err != nil {
			return nil, err
		}
		o.TokenFunc = src.Token
	}
	return odata.New(o)
}

// loginCookie принимает куку со стандартного ввода и кладёт её в файл.
// Через ввод, а не аргументом: иначе кука осядет в истории командной строки.
// loginCookie сохраняет куку RX. src: paste (буфер обмена), stdin (стандартный ввод)
// или пусто (вставить в терминал либо нажать Enter и взять из буфера).
func loginCookie(src string) error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	if cfg.URL == "" {
		return errors.New("не задан адрес RX: выполните rxmcp setup")
	}
	var line string
	switch src {
	case "paste":
		line, err = clipboard()
		if err != nil {
			return err
		}
		fmt.Println("Взял куку из буфера обмена.")
	case "stdin":
		if line, err = readStdin(); err != nil {
			return err
		}
	default:
		printCookieHelp(cfg.URL)
		fmt.Print("Вставьте куку и нажмите Enter (или просто Enter, если она в буфере обмена): ")
		line, err = readLong(bufio.NewReader(os.Stdin))
		if err != nil && line == "" {
			return err
		}
		if line = cookieOrClipboard(line); line == "" {
			return errors.New("куки нет: скопируйте значение sungero_client и повторите rxmcp login")
		}
	}
	if err := auth.SaveCookie(cfg.URL, line); err != nil {
		return err
	}
	fmt.Printf("Сохранено в %s\n", auth.CookieFile(cfg.URL))
	if h := cookieHint(line); h != "" {
		fmt.Println(h)
	}
	if cfg.Auth != "cookie" {
		prof, err := config.LoadFile()
		if err == nil {
			prof["RXMCP_AUTH"] = "cookie"
			if err := config.SaveFile(prof); err == nil {
				fmt.Println("Способ входа в профиле переключён на cookie.")
			}
		}
	}
	fmt.Println("Проверка:")
	os.Setenv("RXMCP_AUTH", "cookie")
	os.Unsetenv("RXMCP_COOKIE")
	return check()
}

// loginSSO сохраняет куки сессии провайдера входа и сразу получает по ним куку RX.
// Дальше rxmcp перевыпускает куку RX сам на каждом 401, пока провайдер не попросит
// войти заново.
func loginSSO() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	if cfg.URL == "" {
		return errors.New("не задан адрес RX: выполните rxmcp setup")
	}
	root, err := weblogin.Root(cfg.URL)
	if err != nil {
		return err
	}
	tr, err := odata.NewTransport(cfg.InsecureTLS, cfg.CAFile)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	scope, err := weblogin.Discover(ctx, tr, root)
	if err != nil {
		return err
	}
	fmt.Printf(`Вход RX идёт через %s
1. Откройте в браузере %saccount (вы уже вошли, пароль не спросят).
2. F12 → Network → обновите страницу → первый запрос → Request Headers → cookie → Copy value.
`, scope, scope)
	fmt.Print("Вставьте значение и нажмите Enter (или просто Enter, если оно в буфере обмена): ")
	line, err := readLong(bufio.NewReader(os.Stdin))
	if err != nil && line == "" {
		return err
	}
	if line = cookieOrClipboard(line); line == "" {
		return errors.New("кук провайдера нет: скопируйте заголовок cookie и повторите rxmcp login --sso")
	}
	cookies, err := weblogin.ParseProviderCookies(line)
	if err != nil {
		return err
	}
	st := &weblogin.State{URL: scope, Cookies: cookies, Updated: time.Now()}
	ck, err := weblogin.Login(ctx, tr, root, st)
	if err != nil {
		return err
	}
	sf := weblogin.File(auth.Dir(), cfg.URL)
	if err := weblogin.Save(sf, st); err != nil {
		return err
	}
	if err := auth.SaveCookie(cfg.URL, ck); err != nil {
		return err
	}
	fmt.Printf("Сессия провайдера сохранена в %s, новая кука RX получена.\n", sf)
	if cfg.Auth != "cookie" {
		if prof, err := config.LoadFile(); err == nil {
			prof["RXMCP_AUTH"] = "cookie"
			if err := config.SaveFile(prof); err == nil {
				fmt.Println("Способ входа в профиле переключён на cookie.")
			}
		}
	}
	fmt.Println("Проверка:")
	os.Setenv("RXMCP_AUTH", "cookie")
	os.Unsetenv("RXMCP_COOKIE")
	return check()
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// cookieHint предупреждает о частой ошибке: в списке куки RX берут external_identity
// вместо sungero_client. Обе выглядят как длинная строка base64, но сессию держит вторая,
// и она заметно длиннее. Значение всё равно сохраняем: размеры зависят от системы.
func cookieHint(cookie string) string {
	v := strings.TrimSpace(cookie)
	if i := strings.Index(v, "sungero_client="); i >= 0 {
		v = v[i+len("sungero_client="):]
	}
	if j := strings.IndexAny(v, ";"); j >= 0 {
		v = v[:j]
	}
	v = strings.TrimSpace(v)
	if len(v) >= 1200 {
		return ""
	}
	return "Внимание: значение короткое (" + fmt.Sprint(len(v)) + " символов). У sungero_client оно обычно вдвое длиннее.\n" +
		"Если проверка ниже не пройдёт, скорее всего скопирована соседняя кука external_identity.\n" +
		"Надёжный способ: F12 → Network → любой запрос к RX → Request Headers → строка cookie → Copy value,\n" +
		"затем rxmcp login --paste: строку с несколькими куками программа принимает целиком."
}

func login() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	if cfg.OIDCIssuer == "" || cfg.OIDCClientID == "" {
		return errors.New("login: задайте RXMCP_OIDC_ISSUER и RXMCP_OIDC_CLIENT_ID (и RXMCP_AUTH=oidc для работы сервера)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	oc := oidcConfig(cfg)
	var t *auth.Tokens
	if cfg.OIDCFlow == "device" {
		t, err = oc.DeviceLogin(ctx, os.Stdout)
	} else {
		t, err = oc.Login(ctx, os.Stdout)
	}
	if err != nil {
		return err
	}
	fmt.Printf("Вход выполнен, токены сохранены в %s\n", auth.DefaultCacheFile(cfg.OIDCIssuer, cfg.OIDCClientID))
	if cl := auth.Claims(t.AccessToken); cl != nil {
		for _, k := range []string{"preferred_username", "name", "email", "aud", "azp"} {
			if v, ok := cl[k]; ok {
				fmt.Printf("  %s: %v\n", k, v)
			}
		}
	}
	fmt.Printf("Токен действует до %s", t.ExpiresAt.Format("15:04:05"))
	if t.RefreshToken != "" {
		fmt.Print(", обновляется автоматически")
	}
	fmt.Println(".")
	if cfg.URL != "" {
		fmt.Println("Проверка доступа к RX: RXMCP_AUTH=oidc rxmcp check")
	}
	return nil
}

// call делает один POST действия модуля. Только для отладки.
func call(args []string) error {
	if len(args) < 1 {
		return errors.New("rxmcp call Module/Action ['{\"param\":1}']")
	}
	cfg, _, err := build()
	if err != nil {
		return err
	}
	cl, err := newClient(cfg)
	if err != nil {
		return err
	}
	module, action, ok := strings.Cut(args[0], "/")
	if !ok {
		return errors.New("формат: Module/Action, например Docflow/CompleteAssignment")
	}
	params := map[string]any{}
	if len(args) > 1 {
		if err := json.Unmarshal([]byte(args[1]), &params); err != nil {
			return fmt.Errorf("параметры не JSON: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	data, err := cl.Action(ctx, module, action, params)
	if err != nil {
		return err
	}
	fmt.Printf("%d байт\n", len(data))
	limit := 2000
	if v := os.Getenv("RXMCP_QUERY_LIMIT"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	if len(data) > limit {
		data = data[:limit]
	}
	fmt.Println(string(data))
	return nil
}

func serve(httpMode bool) error {
	log := logger()
	cfg, svc, err := build()
	if err != nil {
		return err
	}
	if httpMode && cfg.HTTPAddr == "" {
		return errors.New("serve --http: задайте RXMCP_HTTP_ADDR и RXMCP_HTTP_SECRET")
	}
	helpSrc, err := helpSource(cfg)
	if err != nil {
		return err
	}
	helpClient, err := newClient(cfg)
	if err != nil {
		return err
	}
	srv := server.New(svc, server.Options{Version: version, AllowWrite: cfg.AllowWrite, NoConfirm: !cfg.Confirm, MaxTextChars: cfg.MaxTextChars, Logger: log, Help: helpSrc, OData: helpClient,
		StateDir: auth.Dir(), StateKey: stateKey(cfg)})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mode := "read-only"
	if cfg.AllowWrite {
		mode = "read-write"
	}
	if cfg.UpdateCheck {
		go func() {
			if v := availableUpdate(10 * time.Second); v != "" {
				log.Info("new version available", "current", version, "latest", v)
				srv.SetUpdate(v)
			}
		}()
	}
	if !httpMode {
		log.Info("rxmcp started", "version", version, "transport", "stdio", "rx", cfg.ODataURL(), "mode", mode)
		return srv.MCP.Run(ctx, &mcp.StdioTransport{})
	}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv.MCP }, &mcp.StreamableHTTPOptions{Logger: log})
	secret := cfg.HTTPSecret
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Сравнение за постоянное время: по задержке ответа секрет не подобрать.
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(secret)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	hs := &http.Server{Addr: cfg.HTTPAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	log.Info("rxmcp started", "version", version, "transport", "http", "addr", cfg.HTTPAddr, "path", "/mcp", "rx", cfg.ODataURL(), "mode", mode)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// check делает только GET-запросы и печатает, что работает.
func check() error {
	cfg, svc, err := build()
	if err != nil {
		return err
	}
	cl, err := newClient(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	fmt.Printf("rxmcp %s · проверка подключения к %s (auth=%s)\n\n", version, cfg.ODataURL(), cfg.Auth)
	okc, bad := 0, 0
	step := func(name string, fn func() (string, error)) {
		start := time.Now()
		res, err := fn()
		d := time.Since(start).Round(time.Millisecond)
		if err != nil {
			bad++
			fmt.Printf("  ✗ %-28s %v (%s)\n", name, err, d)
			return
		}
		okc++
		fmt.Printf("  ✓ %-28s %s (%s)\n", name, res, d)
	}
	step("сервис интеграции", func() (string, error) {
		n, err := cl.Metadata(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("$metadata %d КБ", n/1024), nil
	})
	var me *rx.Me
	step("кто я", func() (string, error) {
		m, err := svc.WhoAmI(ctx)
		if err != nil {
			return "", err
		}
		me = m
		// Тип входа RX отдаёт не всегда (у учёток из внешнего провайдера Login приходит null).
		// Молчим об этом вместо «вход ?»: знак вопроса читается как поломка.
		if m.LoginType == "" {
			return fmt.Sprintf("%s (id %d)", m.Name, m.ID), nil
		}
		return fmt.Sprintf("%s (id %d, вход %s)", m.Name, m.ID, m.LoginType), nil
	})
	if me != nil {
		step("мои задания в работе", func() (string, error) {
			items, total, err := svc.MyAssignments(ctx, rx.AssignmentFilter{Limit: 3})
			if err != nil {
				return "", err
			}
			t := len(items)
			if total != nil {
				t = int(*total)
			}
			return fmt.Sprintf("%d", t), nil
		})
		step("просроченные", func() (string, error) {
			items, total, err := svc.MyAssignments(ctx, rx.AssignmentFilter{Status: "overdue", Limit: 1})
			if err != nil {
				return "", err
			}
			t := len(items)
			if total != nil {
				t = int(*total)
			}
			return fmt.Sprintf("%d", t), nil
		})
		step("мои задачи", func() (string, error) {
			items, total, err := svc.ListTasks(ctx, rx.TaskFilter{Status: "all", Limit: 1})
			if err != nil {
				return "", err
			}
			t := len(items)
			if total != nil {
				t = int(*total)
			}
			return fmt.Sprintf("%d", t), nil
		})
	}
	var doc *rx.Document
	step("поиск документов", func() (string, error) {
		items, total, err := svc.FindDocuments(ctx, rx.DocumentFilter{CreatedFrom: "2000-01-01", Limit: 1})
		if err != nil {
			return "", err
		}
		if len(items) > 0 {
			doc = &items[0]
		}
		t := len(items)
		if total != nil {
			t = int(*total)
		}
		return fmt.Sprintf("официальных документов доступно: %d", t), nil
	})
	step("поиск по названию (contains)", func() (string, error) {
		_, _, err := svc.FindDocuments(ctx, rx.DocumentFilter{Query: "а", Limit: 1})
		if err != nil {
			return "", err
		}
		return "ок", nil
	})
	if doc != nil {
		step("карточка документа", func() (string, error) {
			d, err := svc.GetDocument(ctx, doc.ID)
			if err != nil {
				return "", err
			}
			doc = d
			return fmt.Sprintf("#%d, версий %d", d.ID, len(d.Versions)), nil
		})
		if len(doc.Versions) > 0 {
			step("тело версии", func() (string, error) {
				data, ext, v, err := svc.VersionBody(ctx, doc, 0)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("версия %d, %s, %d КБ", v.Number, orQ(ext), len(data)/1024), nil
			})
		}
	}
	step("сотрудники", func() (string, error) {
		items, err := svc.FindEmployees(ctx, "а", false, 1)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("поиск работает, найдено %d", len(items)), nil
	})
	fmt.Printf("\nИтог: %d ок, %d с ошибками.", okc, bad)
	if cfg.AllowWrite {
		fmt.Print(" Запись включена (RXMCP_ALLOW_WRITE=1), в проверке не используется.")
		if !cfg.Confirm {
			fmt.Print(" Подтверждение формой выключено (RXMCP_CONFIRM=0).")
		}
	} else {
		fmt.Print(" Режим только чтение.")
	}
	fmt.Println()
	if v := availableUpdate(5 * time.Second); v != "" {
		fmt.Printf("Вышла rxmcp %s (у вас %s): rxmcp update\n", v, version)
	}
	if bad > 0 {
		return errors.New("часть проверок не прошла")
	}
	return nil
}

func orQ(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func install() error {
	exe, err := os.Executable()
	if err != nil {
		exe = "rxmcp"
	}
	exe, _ = filepath.Abs(exe)
	cfgJSON, _ := json.MarshalIndent(map[string]any{
		"mcpServers": map[string]any{"rx": map[string]any{"command": exe}},
	}, "", "  ")
	paths := []string{}
	for _, t := range clientConfigs() {
		paths = append(paths, t.title+": "+t.path)
	}
	fmt.Printf(`Обычно это делает `+"`rxmcp setup`"+` сам. Ниже то же вручную.

Адрес RX, логин и секреты лежат в профиле %s,
поэтому в конфиг клиента идёт только путь к бинарю:

%s

Куда положить:
  %s

Claude Code:
  claude mcp add rx --scope user -- %s

После правки перезапустите Claude Desktop или Cursor (Claude Code подхватывает сам).
Запись (карточки, задачи, выполнение заданий): rxmcp config set RXMCP_ALLOW_WRITE=1
Инструкция с картинками: https://drxinfra.ru/rxmcp#start
`, config.FilePath(), cfgJSON, strings.Join(paths, "\n  "), exe)
	return nil
}

// query делает один GET-запрос и печатает начало ответа. Только для отладки.
func query(args []string) error {
	if len(args) == 0 {
		return errors.New("rxmcp query SET ['$filter=…'] ['$top=1'] …")
	}
	cfg, _, err := build()
	if err != nil {
		return err
	}
	cl, err := newClient(cfg)
	if err != nil {
		return err
	}
	path := args[0]
	if len(args) > 1 {
		var parts []string
		for _, a := range args[1:] {
			k, v, _ := strings.Cut(a, "=")
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
		path += "?" + strings.Join(parts, "&")
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	data, ct, err := cl.Raw(ctx, path)
	if err != nil {
		return err
	}
	fmt.Printf("%s · %d байт\n", ct, len(data))
	limit := 2000
	if v := os.Getenv("RXMCP_QUERY_LIMIT"); v != "" {
		fmt.Sscanf(v, "%d", &limit)
	}
	if len(data) > limit {
		data = data[:limit]
	}
	fmt.Println(string(data))
	return nil
}

// helpSource описывает, где лежит индекс справки этого стенда и как её скачать.
func helpSource(cfg *config.Config) (*server.HelpSource, error) {
	cl, err := newClient(cfg)
	if err != nil {
		return nil, err
	}
	dir, err := auth.EnsureDir()
	if err != nil {
		return nil, err
	}
	return &server.HelpSource{Path: help.Path(dir, cfg.URL), Base: cfg.HelpBase(), Fetch: cl.Abs}, nil
}

// docs работает со справкой стенда из командной строки: index, search, topic, status.
func docs(args []string) error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	hs, err := helpSource(cfg)
	if err != nil {
		return err
	}
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "index":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		fmt.Fprintf(os.Stderr, "Скачиваю справку с %s\n", hs.Base)
		start := time.Now()
		ix, err := help.Crawl(ctx, hs.Fetch, hs.Base, 6, func(done, total int) {
			if done%200 == 0 || done == total {
				fmt.Fprintf(os.Stderr, "\r  %d из %d статей", done, total)
			}
		})
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		if err := ix.Save(hs.Path); err != nil {
			return err
		}
		st, _ := os.Stat(hs.Path)
		fmt.Printf("%s: %d статей за %s, индекс %s (%.1f МБ).\nПомощник найдёт справку сам, перезапускать его не нужно.\n",
			ix.Product, len(ix.Topics), time.Since(start).Round(time.Second), hs.Path, float64(st.Size())/1e6)
		return nil
	case "status":
		ix, err := help.Load(hs.Path)
		if errors.Is(err, help.ErrNoIndex) {
			fmt.Printf("Справка не скачана. Выполните: rxmcp docs index (источник %s)\n", hs.Base)
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Printf("%s: %d статей, скачана %s с %s\nФайл: %s\n", ix.Product, len(ix.Topics), ix.Built.Format("02.01.2006 15:04"), ix.Base, hs.Path)
		return nil
	case "search", "topic":
		if len(args) < 2 {
			return fmt.Errorf("rxmcp docs %s: что искать?", sub)
		}
		ix, err := help.Load(hs.Path)
		if err != nil {
			return fmt.Errorf("%w: выполните rxmcp docs index", err)
		}
		if sub == "topic" {
			t := ix.Topic(args[1])
			if t == nil {
				return fmt.Errorf("статьи %q нет", args[1])
			}
			fmt.Printf("%s\n%s\n%s\n\n%s\n", t.Title, strings.Join(t.Crumbs, " > "), ix.URL(t), t.Text)
			return nil
		}
		for i, h := range ix.Search(strings.Join(args[1:], " "), "", 8) {
			fmt.Printf("%d. %s  [%s]\n   %s\n   %s\n", i+1, h.Topic.Title, h.Topic.File, strings.Join(h.Topic.Crumbs, " > "), h.Snippet)
		}
		return nil
	}
	return fmt.Errorf("rxmcp docs: неизвестная команда %q (есть index, search, topic, status)", sub)
}

// stateKey ключ отметок сервера: хост RX и логин, чтобы разные стенды и учётки
// в одном каталоге настроек не путались.
func stateKey(cfg *config.Config) string {
	host := cfg.URL
	if u, err := url.Parse(cfg.URL); err == nil && u.Host != "" {
		host = u.Host
	}
	if cfg.Login != "" {
		return host + "-" + cfg.Login
	}
	return fmt.Sprintf("%s-%d", host, cfg.UserID)
}
