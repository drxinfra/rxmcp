package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/drxinfra/rxmcp/internal/auth"
)

// Настройки живут в своём файле, а не в конфиге Claude. Причина простая:
// в конфиге клиента правка означает поиск JSON, перезапуск приложения и секрет
// в файле, который лежит без прав 0600. Файл профиля читается на старте сервера,
// поэтому поменять адрес, логин или режим записи можно одной командой.
//
// Формат — тот же набор ключей, что и переменные окружения, чтобы не заводить
// второй язык настроек. Окружение имеет приоритет над файлом.

// FilePath путь к файлу профиля.
func FilePath() string { return filepath.Join(auth.Dir(), "config.json") }

// Known перечисляет ключи, которые имеет смысл держать в профиле.
// Список нужен, чтобы `rxmcp config set` не принимал опечатки молча.
var Known = []string{
	"RXMCP_URL", "RXMCP_AUTH", "RXMCP_LOGIN", "RXMCP_PASSWORD", "RXMCP_TOKEN", "RXMCP_COOKIE",
	"RXMCP_OIDC_ISSUER", "RXMCP_OIDC_CLIENT_ID", "RXMCP_OIDC_CLIENT_SECRET", "RXMCP_OIDC_SCOPE",
	"RXMCP_OIDC_FLOW", "RXMCP_OIDC_PORT", "RXMCP_USER_ID", "RXMCP_ALLOW_WRITE", "RXMCP_INSECURE_TLS",
	"RXMCP_CA", "RXMCP_TIMEOUT", "RXMCP_MAX_TEXT", "RXMCP_TZ", "RXMCP_HTTP_ADDR", "RXMCP_HTTP_SECRET",
}

// Secret сообщает, что значение ключа нельзя печатать.
func Secret(key string) bool {
	switch key {
	case "RXMCP_PASSWORD", "RXMCP_TOKEN", "RXMCP_COOKIE", "RXMCP_OIDC_CLIENT_SECRET", "RXMCP_HTTP_SECRET":
		return true
	}
	return false
}

// LoadFile читает профиль. Отсутствующий файл — не ошибка, битый — ошибка:
// молча работать не с теми настройками хуже, чем не запуститься.
func LoadFile() (map[string]string, error) {
	b, err := os.ReadFile(FilePath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	raw := map[string]any{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w (проверьте JSON или удалите файл и выполните rxmcp setup)", FilePath(), err)
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		key := strings.ToUpper(strings.TrimSpace(k))
		if !strings.HasPrefix(key, "RXMCP_") {
			key = "RXMCP_" + key
		}
		switch t := v.(type) {
		case string:
			out[key] = t
		case bool:
			if t {
				out[key] = "1"
			} else {
				out[key] = "0"
			}
		case float64:
			out[key] = fmt.Sprintf("%g", t)
		case nil:
		default:
			return nil, fmt.Errorf("%s: ключ %s должен быть строкой", FilePath(), key)
		}
	}
	return out, nil
}

// SaveFile перезаписывает профиль правами 0600 (там может лежать пароль или кука).
func SaveFile(m map[string]string) error {
	dir, err := auth.EnsureDir()
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if v != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("{\n")
	for i, k := range keys {
		kv, _ := json.Marshal(k)
		vv, _ := json.Marshal(m[k])
		fmt.Fprintf(&b, "  %s: %s", kv, vv)
		if i < len(keys)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	tmp := filepath.Join(dir, ".config.json.tmp")
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, FilePath())
}
