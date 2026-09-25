package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/drxinfra/rxmcp/internal/auth"
	"github.com/drxinfra/rxmcp/internal/config"
)

// setup — единственная команда, которую человек выполняет руками.
// Она спрашивает адрес, логин и способ входа, кладёт это в профиль (0600),
// проверяет подключение и прописывает сервер в клиенты (Claude Code, Claude Desktop, Cursor).
// В конфиге клиента остаётся только путь к бинарю: ни секретов, ни переменных.

type setupFlags struct {
	url, login, authMode, tz, name string
	write, noClients, yes          bool
}

func setup(args []string) error {
	f := setupFlags{name: "rx"}
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch {
		case a == "--url":
			f.url = next()
		case a == "--login":
			f.login = next()
		case a == "--auth":
			f.authMode = next()
		case a == "--tz":
			f.tz = next()
		case a == "--name":
			f.name = next()
		case a == "--write":
			f.write = true
		case a == "--no-clients":
			f.noClients = true
		case a == "--yes", a == "-y":
			f.yes = true
		case strings.HasPrefix(a, "--url="):
			f.url = strings.TrimPrefix(a, "--url=")
		case strings.HasPrefix(a, "--login="):
			f.login = strings.TrimPrefix(a, "--login=")
		case strings.HasPrefix(a, "--auth="):
			f.authMode = strings.TrimPrefix(a, "--auth=")
		case strings.HasPrefix(a, "--tz="):
			f.tz = strings.TrimPrefix(a, "--tz=")
		case strings.HasPrefix(a, "--name="):
			f.name = strings.TrimPrefix(a, "--name=")
		default:
			return fmt.Errorf("setup: неизвестный аргумент %q", a)
		}
	}

	prof, err := config.LoadFile()
	if err != nil {
		return err
	}
	in := bufio.NewReader(os.Stdin)
	tty := isTTY()

	fmt.Println("Настройка rxmcp. Ответы сохранятся в", config.FilePath())
	fmt.Println()

	// Адрес.
	rxURL := f.url
	if rxURL == "" {
		rxURL = prof["RXMCP_URL"]
	}
	if rxURL == "" && !tty {
		return errors.New("setup: не задан --url (в неинтерактивном режиме спросить некого)")
	}
	if f.url == "" && tty {
		rxURL = ask(in, "Адрес RX (например https://rx.company.ru)", rxURL)
	}
	rxURL, err = normalizeRX(rxURL)
	if err != nil {
		return err
	}
	prof["RXMCP_URL"] = rxURL

	// Логин.
	login := f.login
	if login == "" {
		login = prof["RXMCP_LOGIN"]
	}
	if f.login == "" && tty {
		login = ask(in, "Логин в RX", login)
	}
	if login == "" {
		return errors.New("setup: нужен логин в RX (--login): по нему сервер находит ваши задания")
	}
	prof["RXMCP_LOGIN"] = login

	// Способ входа.
	mode := strings.ToLower(f.authMode)
	if mode == "" {
		mode = strings.ToLower(prof["RXMCP_AUTH"])
	}
	if f.authMode == "" && tty {
		def := mode
		if def == "" {
			def = "cookie"
		}
		fmt.Println()
		fmt.Println("Как входить в RX?")
		fmt.Println("  password — логин и пароль RX. Подходит, если вход в RX по паролю.")
		fmt.Println("  cookie   — кука из браузера. Подходит всегда, в том числе SSO, Keycloak, домен.")
		fmt.Println("  oidc     — вход в браузере через ваш провайдер, нужен заведённый клиент.")
		mode = strings.ToLower(ask(in, "Способ входа (password/cookie/oidc)", def))
	}
	switch mode {
	case "", "cookie":
		mode = "cookie"
	case "password", "basic":
		mode = "basic"
	case "oidc":
	default:
		return fmt.Errorf("setup: --auth=%s: допустимо password, cookie, oidc", mode)
	}
	prof["RXMCP_AUTH"] = mode

	// Секрет под выбранный способ.
	switch mode {
	case "basic":
		pw := prof["RXMCP_PASSWORD"]
		if v := os.Getenv("RXMCP_PASSWORD"); v != "" {
			pw = v // передали переменной — забираем в профиль, чтобы клиенту его не знать
		}
		if tty {
			prompt := "Пароль RX"
			if pw != "" {
				prompt = "Пароль RX (Enter — оставить сохранённый)"
			}
			got, err := askSecret(in, prompt)
			if err != nil {
				return err
			}
			if got != "" {
				pw = got
			}
		}
		if pw == "" {
			return errors.New("setup: нужен пароль: передайте его в RXMCP_PASSWORD или запустите setup в терминале")
		}
		prof["RXMCP_PASSWORD"] = pw
		delete(prof, "RXMCP_COOKIE")
	case "cookie":
		delete(prof, "RXMCP_PASSWORD")
		delete(prof, "RXMCP_COOKIE") // кука живёт в своём файле, не в профиле
		if _, err := auth.LoadCookie(rxURL); err != nil {
			if !tty {
				fmt.Println("Куки нет. Выполните позже: rxmcp login")
			} else {
				fmt.Println()
				printCookieHelp(rxURL)
				c := ask(in, "Кука", "")
				if strings.TrimSpace(c) == "" {
					fmt.Println("Пропускаю. Когда будет кука: rxmcp login")
				} else if err := auth.SaveCookie(rxURL, c); err != nil {
					return err
				}
			}
		} else {
			fmt.Println("Кука уже сохранена:", auth.CookieFile(rxURL), "(обновить: rxmcp login)")
		}
	case "oidc":
		if prof["RXMCP_OIDC_ISSUER"] == "" && tty {
			prof["RXMCP_OIDC_ISSUER"] = ask(in, "Адрес realm (например https://sso.company.ru/realms/company)", "")
		}
		if prof["RXMCP_OIDC_CLIENT_ID"] == "" && tty {
			prof["RXMCP_OIDC_CLIENT_ID"] = ask(in, "client_id", "")
		}
	}

	// Часовой пояс и запись.
	if f.tz != "" {
		prof["RXMCP_TZ"] = f.tz
	} else if prof["RXMCP_TZ"] == "" {
		if tzn, _ := time.Now().Zone(); tzn != "" {
			if name := localZoneName(); name != "" {
				prof["RXMCP_TZ"] = name
			}
		}
	}
	if f.write {
		prof["RXMCP_ALLOW_WRITE"] = "1"
	} else if tty && !f.yes {
		fmt.Println()
		fmt.Println("Запись: карточки на досках, задачи, выполнение заданий. Каждое действие спрашивает подтверждение.")
		if yes(in, "Разрешить запись?", prof["RXMCP_ALLOW_WRITE"] == "1") {
			prof["RXMCP_ALLOW_WRITE"] = "1"
		} else {
			delete(prof, "RXMCP_ALLOW_WRITE")
		}
	}

	if err := config.SaveFile(prof); err != nil {
		return err
	}
	fmt.Println()
	fmt.Println("Профиль записан:", config.FilePath())

	// Проверка подключения: только чтение.
	fmt.Println()
	if err := check(); err != nil {
		fmt.Println()
		fmt.Println("Подключиться не удалось:", err)
		fmt.Println("Настройки сохранены. Поправьте и повторите: rxmcp check")
	}

	if f.noClients {
		return nil
	}
	fmt.Println()
	return registerClients(f.name, f.yes || !tty, in)
}

// normalizeRX приводит адрес к корню сервиса интеграции.
func normalizeRX(s string) (string, error) {
	s = strings.TrimSpace(strings.TrimRight(s, "/"))
	if s == "" {
		return "", errors.New("setup: пустой адрес RX")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("setup: не разобрать адрес %q", s)
	}
	p := strings.TrimRight(u.Path, "/")
	low := strings.ToLower(p)
	switch {
	case strings.HasSuffix(low, "/odata"):
		p = p[:len(p)-len("/odata")]
	case strings.HasSuffix(low, "/$metadata"):
		p = p[:len(p)-len("/$metadata")]
		p = strings.TrimSuffix(p, "/odata")
	}
	if p == "" {
		p = "/integration"
	}
	u.Path = p
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

func printCookieHelp(rxURL string) {
	host := rxURL
	if u, err := url.Parse(rxURL); err == nil && u.Host != "" {
		host = u.Scheme + "://" + u.Host
	}
	fmt.Println("Где взять куку (делается один раз, потом обновлять командой rxmcp login):")
	fmt.Println("  1. Откройте", host, "и войдите как обычно.")
	fmt.Println("  2. F12 → Application (в Firefox Хранилище) → Cookies → выберите адрес.")
	fmt.Println("  3. Скопируйте значение sungero_client и вставьте сюда.")
}

// registerClients прописывает сервер в MCP-клиенты. В запись клиента идёт только путь
// к бинарю: адрес, логин и секреты берутся из профиля rxmcp.
func registerClients(name string, auto bool, in *bufio.Reader) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.Abs(exe)

	done := []string{}
	// Claude Code: через свой CLI, чтобы не лезть в ~/.claude.json руками.
	if cc, err := exec.LookPath("claude"); err == nil {
		if auto || yes(in, "Прописать в Claude Code?", true) {
			_ = exec.Command(cc, "mcp", "remove", name, "--scope", "user").Run()
			out, err := exec.Command(cc, "mcp", "add", name, "--scope", "user", "--", exe).CombinedOutput()
			if err != nil {
				fmt.Printf("Claude Code: не получилось (%v): %s\n", err, strings.TrimSpace(string(out)))
				fmt.Printf("Сделайте вручную: claude mcp add %s --scope user -- %s\n", name, exe)
			} else {
				done = append(done, "Claude Code")
			}
		}
	}
	for _, t := range clientConfigs() {
		if _, err := os.Stat(filepath.Dir(t.path)); err != nil {
			continue // клиент не установлен
		}
		if !auto && !yes(in, "Прописать в "+t.title+"?", true) {
			continue
		}
		if err := mergeMCPConfig(t.path, name, exe); err != nil {
			fmt.Printf("%s: %v\n", t.title, err)
			continue
		}
		done = append(done, t.title)
	}
	if len(done) == 0 {
		fmt.Println("MCP-клиентов на машине не нашёл. Команда для ручной настройки:")
		fmt.Printf("  claude mcp add rx --scope user -- %s\n", exe)
		return nil
	}
	fmt.Println("Прописан в:", strings.Join(done, ", "))
	fmt.Println("Claude Desktop и Cursor надо перезапустить, Claude Code подхватит сам.")
	return nil
}

type clientTarget struct{ title, path string }

func clientConfigs() []clientTarget {
	home, _ := os.UserHomeDir()
	var out []clientTarget
	switch runtime.GOOS {
	case "darwin":
		out = append(out, clientTarget{"Claude Desktop", filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")})
	case "windows":
		out = append(out, clientTarget{"Claude Desktop", filepath.Join(os.Getenv("APPDATA"), "Claude", "claude_desktop_config.json")})
	default:
		out = append(out, clientTarget{"Claude Desktop", filepath.Join(home, ".config", "Claude", "claude_desktop_config.json")})
	}
	out = append(out, clientTarget{"Cursor", filepath.Join(home, ".cursor", "mcp.json")})
	return out
}

// mergeMCPConfig добавляет сервер в конфиг клиента, не теряя остальное содержимое.
func mergeMCPConfig(path, name, exe string) error {
	cfg := map[string]any{}
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return fmt.Errorf("%s: не разобрать JSON, правьте вручную (%w)", path, err)
		}
		// Бэкап один раз на запуск: если что-то пойдёт не так, файл рядом.
		_ = os.WriteFile(path+".rxmcp-backup", b, 0o600)
	}
	servers, _ := cfg["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	servers[name] = map[string]any{"command": exe}
	cfg["mcpServers"] = servers
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func ask(in *bufio.Reader, prompt, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", prompt, def)
	} else {
		fmt.Printf("%s: ", prompt)
	}
	line, _ := in.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func yes(in *bufio.Reader, prompt string, def bool) bool {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	fmt.Printf("%s [%s]: ", prompt, d)
	line, _ := in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def
	case "y", "yes", "д", "да":
		return true
	}
	return false
}

// askSecret читает строку без эха, если это возможно.
func askSecret(in *bufio.Reader, prompt string) (string, error) {
	fmt.Printf("%s: ", prompt)
	restore := silenceEcho()
	line, err := in.ReadString('\n')
	restore()
	fmt.Println()
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// silenceEcho глушит эхо терминала через stty; на Windows и без tty просто ничего не делает.
func silenceEcho() func() {
	if runtime.GOOS == "windows" || !isTTY() {
		return func() {}
	}
	stty, err := exec.LookPath("stty")
	if err != nil {
		return func() {}
	}
	run := func(arg string) {
		c := exec.Command(stty, arg)
		c.Stdin = os.Stdin
		_ = c.Run()
	}
	run("-echo")
	return func() { run("echo") }
}

// isTTY отвечает, есть ли с кем разговаривать. Проверки на символьное устройство
// не хватает: /dev/null тоже символьное, поэтому на unix уточняем через stty.
var (
	ttyOnce sync.Once
	ttyVal  bool
)

func isTTY() bool {
	ttyOnce.Do(func() {
		fi, err := os.Stdin.Stat()
		if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
			return
		}
		if runtime.GOOS == "windows" {
			ttyVal = true
			return
		}
		if p, err := exec.LookPath("stty"); err == nil {
			c := exec.Command(p, "-a")
			c.Stdin = os.Stdin
			ttyVal = c.Run() == nil
			return
		}
		ttyVal = true
	})
	return ttyVal
}

// localZoneName достаёт имя пояса в формате IANA (Europe/Moscow), если система его знает.
func localZoneName() string {
	if l := time.Local.String(); l != "" && l != "Local" && l != "UTC" {
		return l
	}
	if b, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(b, "zoneinfo/"); i >= 0 {
			return b[i+len("zoneinfo/"):]
		}
	}
	return ""
}
