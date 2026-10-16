package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"

	"github.com/drxinfra/rxmcp/internal/auth"
	"github.com/drxinfra/rxmcp/internal/config"
)

// configCmd показывает и правит профиль. Секреты не печатаются никогда:
// в логах и скриншотах они не нужны, а факт «задано» виден по слову «задано».
func configCmd(args []string) error {
	prof, err := config.LoadFile()
	if err != nil {
		return err
	}
	if len(args) == 0 {
		fmt.Println("Профиль:", config.FilePath())
		fmt.Println("Каталог настроек:", auth.Dir())
		if len(prof) == 0 {
			fmt.Println("(пусто — выполните rxmcp setup)")
			return nil
		}
		keys := make([]string, 0, len(prof))
		for k := range prof {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := prof[k]
			if config.Secret(k) {
				v = "задано"
			}
			mark := ""
			if os.Getenv(k) != "" {
				mark = "  (перекрыто переменной окружения)"
			}
			fmt.Printf("  %-24s %s%s\n", k, v, mark)
		}
		if u := prof["RXMCP_URL"]; u != "" {
			if _, err := auth.LoadCookie(u); err == nil {
				fmt.Println("  кука                     сохранена:", auth.CookieFile(u))
			}
		}
		return nil
	}
	switch args[0] {
	case "set":
		if len(args) < 2 {
			return errors.New("rxmcp config set RXMCP_TZ=Europe/Moscow")
		}
		for _, kv := range args[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				return fmt.Errorf("нужно КЛЮЧ=значение, а не %q", kv)
			}
			k = strings.ToUpper(strings.TrimSpace(k))
			if !strings.HasPrefix(k, "RXMCP_") {
				k = "RXMCP_" + k
			}
			if !knownKey(k) {
				return fmt.Errorf("неизвестный ключ %s; список: rxmcp help", k)
			}
			prof[k] = strings.TrimSpace(v)
		}
		return config.SaveFile(prof)
	case "unset":
		if len(args) < 2 {
			return errors.New("rxmcp config unset RXMCP_ALLOW_WRITE")
		}
		for _, k := range args[1:] {
			k = strings.ToUpper(strings.TrimSpace(k))
			if !strings.HasPrefix(k, "RXMCP_") {
				k = "RXMCP_" + k
			}
			delete(prof, k)
		}
		return config.SaveFile(prof)
	case "path":
		fmt.Println(config.FilePath())
		return nil
	}
	return fmt.Errorf("rxmcp config [set КЛЮЧ=значение | unset КЛЮЧ | path]")
}

func knownKey(k string) bool {
	for _, x := range config.Known {
		if x == k {
			return true
		}
	}
	return false
}

// clipboard читает буфер обмена. Нужно ровно для одного случая: куку скопировали
// в браузере, и вставлять её в терминал руками незачем.
// cookieOrClipboard возвращает куку, вставленную в строку, а при пустом вводе берёт её
// из буфера обмена. Строку читает readLong: обычный ввод терминала macOS и Linux
// ограничен 1024 (Linux 4096) символами, а sungero_client бывает длиннее. Содержимое
// буфера, не похожее на куку (короткое или в несколько строк), отбрасывается.
func cookieOrClipboard(line string) string {
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	c, err := clipboard()
	if err != nil {
		fmt.Println("Буфер обмена не прочитан:", err)
		return ""
	}
	if len(c) < 100 || strings.ContainsAny(c, "\r\n") {
		fmt.Println("В буфере обмена не кука: там короткий или многострочный текст.")
		return ""
	}
	fmt.Printf("Взял куку из буфера обмена (%d символов).\n", len(c))
	return c
}

func clipboard() (string, error) {
	var try [][]string
	switch runtime.GOOS {
	case "darwin":
		try = [][]string{{"pbpaste"}}
	case "windows":
		try = [][]string{{"powershell", "-NoProfile", "-Command", "Get-Clipboard"}}
	default:
		try = [][]string{{"wl-paste", "--no-newline"}, {"xclip", "-selection", "clipboard", "-o"}, {"xsel", "-b"}}
		if wsl() {
			// В WSL куку копируют в браузере Windows: буфер Linux о ней не знает, а wl-paste
			// и xclip там обычно нет. Буфер Windows отдаёт powershell.exe; если пути Windows
			// не добавлены в PATH, берём его по полному адресу.
			try = append([][]string{
				{"powershell.exe", "-NoProfile", "-Command", "Get-Clipboard"},
				{"/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe", "-NoProfile", "-Command", "Get-Clipboard"},
			}, try...)
		}
	}
	for _, c := range try {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		out, err := exec.Command(c[0], c[1:]...).Output()
		if err != nil {
			return "", fmt.Errorf("%s: %w", c[0], err)
		}
		s := strings.TrimSpace(string(out))
		if s == "" {
			return "", errors.New("буфер обмена пуст")
		}
		return s, nil
	}
	return "", errors.New("нет команды для чтения буфера обмена (нужен pbpaste, wl-paste, xclip или xsel): вставьте куку в терминал командой rxmcp login или передайте её через rxmcp login --stdin")
}

// wsl отвечает, что rxmcp запущен в Linux внутри Windows (WSL).
func wsl() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	b, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	return strings.Contains(strings.ToLower(string(b)), "microsoft")
}

// readLong читает строку из терминала без предела длины. В каноническом режиме
// терминал macOS не принимает строку длиннее 1024 символов, Linux длиннее 4096:
// вставленная кука молча обрезается или вместо ввода звучит сигнал. На время чтения
// терминал переводится в неканонический режим (символы приходят сразу, Enter завершает
// строку), потом прежние настройки возвращаются. Стирание символов в этом режиме
// не работает, но для вставки оно и не нужно.
func readLong(in *bufio.Reader) (string, error) {
	if runtime.GOOS != "windows" && isTTY() {
		if stty, err := exec.LookPath("stty"); err == nil {
			run := func(args ...string) (string, error) {
				c := exec.Command(stty, args...)
				c.Stdin = os.Stdin
				out, err := c.Output()
				return strings.TrimSpace(string(out)), err
			}
			if saved, err := run("-g"); err == nil && saved != "" {
				if _, err := run("-icanon", "min", "1", "time", "0"); err == nil {
					defer run(saved)
				}
			}
		}
	}
	return in.ReadString('\n')
}

// readStdin читает куку целиком из стандартного ввода: rxmcp login --stdin < cookie.txt
// или powershell.exe Get-Clipboard | rxmcp login --stdin.
func readStdin() (string, error) {
	b, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", errors.New("на стандартном вводе пусто")
	}
	return s, nil
}
