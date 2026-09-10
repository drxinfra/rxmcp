// rxmcp: MCP-сервер для Directum RX.
package main

import (
	"context"
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
	"github.com/drxinfra/rxmcp/internal/odata"
	"github.com/drxinfra/rxmcp/internal/rx"
	"github.com/drxinfra/rxmcp/internal/server"
)

var version = "dev"

const usage = `rxmcp %s: MCP-сервер для Directum RX (drxinfra.ru/rxmcp)

Команды:
  rxmcp                 запустить сервер по stdio (так его вызывает Claude Desktop, Cursor и др.)
  rxmcp serve --http    запустить по HTTP (Streamable HTTP) на RXMCP_HTTP_ADDR, нужен RXMCP_HTTP_SECRET
  rxmcp check           проверить подключение к RX только чтением и показать, что работает
  rxmcp login           войти через OIDC (Keycloak и др.) в браузере и сохранить токены; logout удаляет их
  rxmcp query SET [$k=v …]   один GET к OData для отладки, например: rxmcp query IAssignments '$top=1'
  rxmcp call Module/Action '{json}'   один POST действия для отладки (меняет данные, если действие пишущее)
  rxmcp install         напечатать фрагменты конфигурации для Claude Desktop, Claude Code, Cursor
  rxmcp version

Переменные окружения:
  RXMCP_URL           адрес сервиса интеграции, например https://rx.company.ru/Integration
  RXMCP_LOGIN         логин пользователя RX (тип входа «пароль»)
  RXMCP_PASSWORD      пароль
  RXMCP_AUTH          basic (по умолчанию) | header | bearer | cookie | oidc
  RXMCP_TOKEN         токен для RXMCP_AUTH=bearer
  RXMCP_COOKIE        заголовок Cookie из браузера после входа в RX (RXMCP_AUTH=cookie)
  RXMCP_OIDC_ISSUER   адрес realm, например https://sso.company.ru/realms/company (RXMCP_AUTH=oidc)
  RXMCP_OIDC_CLIENT_ID, RXMCP_OIDC_CLIENT_SECRET, RXMCP_OIDC_SCOPE, RXMCP_OIDC_PORT
  RXMCP_OIDC_FLOW     code (по умолчанию, вход в браузере) | device (код на экране, для серверов и прокси)
  RXMCP_USER_ID       Id пользователя RX, если его нельзя вычислить по логину
  RXMCP_ALLOW_WRITE   1 = включить инструменты записи (выполнить задание, создать и прекратить задачу)
  RXMCP_INSECURE_TLS  1 = не проверять сертификат RX (только для тестовых стендов)
  RXMCP_CA            файл PEM с корневым сертификатом вашего УЦ
  RXMCP_TIMEOUT       таймаут запроса к RX, по умолчанию 30s
  RXMCP_MAX_TEXT      лимит текста документа в символах, по умолчанию 20000
  RXMCP_TZ            часовой пояс для дат, например Europe/Moscow (по умолчанию системный)
  RXMCP_HTTP_ADDR     адрес для serve --http, например 127.0.0.1:8765
  RXMCP_HTTP_SECRET   общий секрет для HTTP-режима, клиент шлёт заголовок Authorization: Bearer <секрет>
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
		return nil
	case "help", "-h", "--help":
		fmt.Printf(usage, version)
		return nil
	case "install":
		return install()
	case "check":
		return check()
	case "query":
		return query(args)
	case "call":
		return call(args)
	case "login":
		return login()
	case "logout":
		cfg, err := config.FromEnv()
		if err != nil {
			return err
		}
		return oidcConfig(cfg).Logout()
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
	if cfg.Auth == "oidc" {
		src, err := auth.NewSource(oidcConfig(cfg))
		if err != nil {
			return nil, err
		}
		o.TokenFunc = src.Token
	}
	return odata.New(o)
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
	srv := server.New(svc, server.Options{Version: version, AllowWrite: cfg.AllowWrite, MaxTextChars: cfg.MaxTextChars, Logger: log})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mode := "read-only"
	if cfg.AllowWrite {
		mode = "read-write"
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
		if strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") != secret {
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
		return fmt.Sprintf("%s (id %d, вход %s)", m.Name, m.ID, orQ(m.LoginType)), nil
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
	} else {
		fmt.Print(" Режим только чтение.")
	}
	fmt.Println()
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
	env := map[string]string{
		"RXMCP_URL":      "https://rx.company.ru/Integration",
		"RXMCP_LOGIN":    "ivanov",
		"RXMCP_PASSWORD": "***",
	}
	for _, k := range []string{"RXMCP_URL", "RXMCP_LOGIN"} {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}
	cfgJSON, _ := json.MarshalIndent(map[string]any{
		"mcpServers": map[string]any{"rx": map[string]any{"command": exe, "env": env}},
	}, "", "  ")
	fmt.Printf(`Claude Desktop: файл claude_desktop_config.json (Settings → Developer → Edit Config), добавьте:
%s

Claude Code:
  claude mcp add rx -e RXMCP_URL=%s -e RXMCP_LOGIN=%s -e RXMCP_PASSWORD=*** -- %s

Cursor: файл ~/.cursor/mcp.json, тот же JSON, что для Claude Desktop.

Чтобы включить запись (выполнять задания, создавать задачи), добавьте в env "RXMCP_ALLOW_WRITE": "1".
Проверить подключение: RXMCP_URL=… RXMCP_LOGIN=… RXMCP_PASSWORD=… %s check
`, cfgJSON, env["RXMCP_URL"], env["RXMCP_LOGIN"], exe, exe)
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
