package main

import (
	"errors"
	"fmt"
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
func clipboard() (string, error) {
	var try [][]string
	switch runtime.GOOS {
	case "darwin":
		try = [][]string{{"pbpaste"}}
	case "windows":
		try = [][]string{{"powershell", "-NoProfile", "-Command", "Get-Clipboard"}}
	default:
		try = [][]string{{"wl-paste", "--no-newline"}, {"xclip", "-selection", "clipboard", "-o"}, {"xsel", "-b"}}
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
	return "", errors.New("нет команды для чтения буфера обмена (нужен pbpaste, wl-paste, xclip или xsel)")
}
