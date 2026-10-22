package auth

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/drxinfra/rxmcp/internal/secret"
)

// Куку держим в файле рядом с токенами OIDC, а не в конфиге клиента.
// Это даёт две вещи: в настройках Claude нет секрета, и обновление куки
// не требует ни правки JSON, ни перезапуска приложения — сервер перечитывает
// файл на каждом запросе.

// CookieFile путь к файлу с кукой для конкретного адреса RX.
func CookieFile(rxURL string) string {
	host := "rx"
	if u, e := url.Parse(rxURL); e == nil && u.Host != "" {
		host = strings.ReplaceAll(u.Host, ":", "_")
	}
	return filepath.Join(Dir(), "cookie-"+host+".txt")
}

// SaveCookie сохраняет куку: в файл с правами 0600 или в связку ключей ОС.
func SaveCookie(rxURL, cookie string) error {
	cookie = strings.TrimSpace(cookie)
	if cookie == "" {
		return errors.New("пустая строка куки")
	}
	// Из браузера часто копируют с префиксом «Cookie:» — убираем.
	cookie = strings.TrimPrefix(cookie, "Cookie:")
	cookie = strings.TrimSpace(cookie)
	cookie = withName(cookie)
	// Файл пишется через временный и переименование: куку пишут сразу несколько процессов
	// (Claude, Cursor, keepalive), и читающий не должен застать его пустым.
	// С RXMCP_SECRETS=keyring кука уходит в связку ключей ОС.
	return secret.WriteFile(CookieFile(rxURL), []byte(cookie+"\n"))
}

// withName дописывает имя куки, если вставлено одно значение. Проверять по знаку равенства
// нельзя: значение sungero_client это base64, и оно само часто кончается на «=».
// Голым значением считаем строку, где после первого «=» нет ничего, кроме таких же знаков.
func withName(cookie string) string {
	i := strings.Index(cookie, "=")
	if i > 0 && strings.Trim(cookie[i:], "=") != "" {
		return cookie
	}
	return "sungero_client=" + cookie
}

// LoadCookie читает сохранённую куку.
func LoadCookie(rxURL string) (string, error) {
	b, err := secret.ReadFile(CookieFile(rxURL))
	if err != nil {
		return "", err
	}
	c := strings.TrimSpace(string(b))
	if c == "" {
		return "", fmt.Errorf("файл %s пуст", CookieFile(rxURL))
	}
	return c, nil
}

// ForgetCookie удаляет сохранённую куку.
func ForgetCookie(rxURL string) error {
	return secret.Remove(CookieFile(rxURL))
}
