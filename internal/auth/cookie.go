package auth

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

// SaveCookie кладёт куку в файл с правами 0600.
func SaveCookie(rxURL, cookie string) error {
	cookie = strings.TrimSpace(cookie)
	if cookie == "" {
		return errors.New("пустая строка куки")
	}
	// Из браузера часто копируют с префиксом «Cookie:» — убираем.
	cookie = strings.TrimPrefix(cookie, "Cookie:")
	cookie = strings.TrimSpace(cookie)
	if !strings.Contains(cookie, "=") {
		return errors.New("это не похоже на куку: нет знака равенства. Нужна строка вида sungero_client=...")
	}
	f := CookieFile(rxURL)
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return err
	}
	return os.WriteFile(f, []byte(cookie+"\n"), 0o600)
}

// LoadCookie читает сохранённую куку.
func LoadCookie(rxURL string) (string, error) {
	b, err := os.ReadFile(CookieFile(rxURL))
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
	err := os.Remove(CookieFile(rxURL))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
