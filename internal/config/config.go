// Package config читает настройки rxmcp из переменных окружения и флагов.
package config

import (
	"errors"
	"fmt"
	"github.com/drxinfra/rxmcp/internal/auth"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config описывает подключение к RX и режим работы сервера.
type Config struct {
	// URL сервиса интеграции, например https://rx.company.ru/Integration.
	// Хвост /odata добавляется автоматически.
	URL string
	// Способ передачи учётки: basic, header, bearer.
	Auth     string
	Login    string
	Password string
	Token    string
	Cookie   string
	// OIDC: провайдер и клиент.
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCScope        string
	OIDCFlow         string // code (по умолчанию) | device
	OIDCPort         int
	// Id пользователя RX, если его нельзя вычислить по логину (bearer).
	UserID int64
	// Разрешить инструменты записи.
	AllowWrite bool
	// Не проверять TLS-сертификат RX.
	InsecureTLS bool
	// Путь к корневому сертификату, если у RX свой УЦ.
	CAFile  string
	Timeout time.Duration
	// Размер списков по умолчанию и максимум.
	PageSize    int
	MaxPageSize int
	// Лимит текста документа в символах.
	MaxTextChars int
	// Часовой пояс для вывода дат (IANA), пусто = локальный.
	TimeZone string
	// HTTP-режим: адрес и общий секрет.
	HTTPAddr   string
	HTTPSecret string
}

// FromEnv собирает конфигурацию: окружение, а чего в нём нет — из файла профиля.
// Так один и тот же бинарь работает и когда всё передано через env (контейнер, CI),
// и когда в конфиге клиента указан только путь к нему.
func FromEnv() (*Config, error) {
	prof, err := LoadFile()
	if err != nil {
		return nil, err
	}
	get := func(k string) string {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
		return strings.TrimSpace(prof[k])
	}
	c := &Config{
		URL:              strings.TrimRight(get("RXMCP_URL"), "/"),
		Auth:             strings.ToLower(strings.TrimSpace(firstNonEmpty(get("RXMCP_AUTH"), "basic"))),
		Login:            get("RXMCP_LOGIN"),
		Password:         get("RXMCP_PASSWORD"),
		Token:            get("RXMCP_TOKEN"),
		Cookie:           get("RXMCP_COOKIE"),
		OIDCIssuer:       strings.TrimRight(get("RXMCP_OIDC_ISSUER"), "/"),
		OIDCClientID:     get("RXMCP_OIDC_CLIENT_ID"),
		OIDCClientSecret: get("RXMCP_OIDC_CLIENT_SECRET"),
		OIDCScope:        get("RXMCP_OIDC_SCOPE"),
		AllowWrite:       isTrue(get("RXMCP_ALLOW_WRITE")),
		InsecureTLS:      isTrue(get("RXMCP_INSECURE_TLS")),
		CAFile:           get("RXMCP_CA"),
		Timeout:          30 * time.Second,
		PageSize:         20,
		MaxPageSize:      100,
		MaxTextChars:     20000,
		TimeZone:         get("RXMCP_TZ"),
		HTTPAddr:         get("RXMCP_HTTP_ADDR"),
		HTTPSecret:       get("RXMCP_HTTP_SECRET"),
	}
	if v := get("RXMCP_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("RXMCP_TIMEOUT: %w", err)
		}
		c.Timeout = d
	}
	if v := get("RXMCP_OIDC_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("RXMCP_OIDC_PORT: %w", err)
		}
		c.OIDCPort = n
	}
	if v := get("RXMCP_USER_ID"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("RXMCP_USER_ID: %w", err)
		}
		c.UserID = n
	}
	if v := get("RXMCP_MAX_TEXT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1000 {
			return nil, errors.New("RXMCP_MAX_TEXT: число не меньше 1000")
		}
		c.MaxTextChars = n
	}
	return c, nil
}

// Validate проверяет, что подключение к RX задано полностью.
func (c *Config) Validate() error {
	if c.URL == "" {
		return errors.New("не задан адрес RX: выполните `rxmcp setup` или передайте RXMCP_URL (например https://rx.company.ru/Integration)")
	}
	if !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://") {
		return errors.New("RXMCP_URL должен начинаться с http:// или https://")
	}
	switch c.Auth {
	case "basic", "header":
		if c.Login == "" || c.Password == "" {
			return errors.New("не заданы логин и пароль: выполните `rxmcp setup` или передайте RXMCP_LOGIN и RXMCP_PASSWORD")
		}
	case "bearer":
		if c.Token == "" {
			return errors.New("RXMCP_AUTH=bearer требует RXMCP_TOKEN")
		}
	case "cookie":
		if c.Cookie == "" && !c.HasStoredCookie() {
			return errors.New("нет куки: войдите в RX в браузере и выполните `rxmcp login --paste`, либо задайте RXMCP_COOKIE")
		}
	case "oidc":
		if c.OIDCIssuer == "" || c.OIDCClientID == "" {
			return errors.New("RXMCP_AUTH=oidc требует RXMCP_OIDC_ISSUER и RXMCP_OIDC_CLIENT_ID")
		}
	default:
		return fmt.Errorf("RXMCP_AUTH=%q: допустимо basic, header, bearer, cookie, oidc", c.Auth)
	}
	if (c.Auth == "cookie" || c.Auth == "oidc" || c.Auth == "bearer") && c.Login == "" && c.UserID == 0 {
		return fmt.Errorf("при RXMCP_AUTH=%s задайте RXMCP_LOGIN (логин в RX, чтобы найти пользователя) или RXMCP_USER_ID", c.Auth)
	}
	if c.HTTPAddr != "" && c.HTTPSecret == "" {
		return errors.New("HTTP-режим требует RXMCP_HTTP_SECRET")
	}
	return nil
}

// HasStoredCookie сообщает, лежит ли кука в файле (её кладёт `rxmcp login`).
func (c *Config) HasStoredCookie() bool {
	_, err := auth.LoadCookie(c.URL)
	return err == nil
}

// ODataURL возвращает корень OData.
func (c *Config) ODataURL() string {
	u := c.URL
	if strings.HasSuffix(strings.ToLower(u), "/odata") {
		return u
	}
	return u + "/odata"
}

// Location возвращает часовой пояс для вывода дат.
func (c *Config) Location() *time.Location {
	if c.TimeZone != "" {
		if loc, err := time.LoadLocation(c.TimeZone); err == nil {
			return loc
		}
	}
	return time.Local
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func isTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "да":
		return true
	}
	return false
}
