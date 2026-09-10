// Package config читает настройки rxmcp из переменных окружения и флагов.
package config

import (
	"errors"
	"fmt"
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

// FromEnv собирает конфигурацию из окружения. Ошибки только по обязательным полям.
func FromEnv() (*Config, error) {
	c := &Config{
		URL:              strings.TrimRight(os.Getenv("RXMCP_URL"), "/"),
		Auth:             strings.ToLower(strings.TrimSpace(env("RXMCP_AUTH", "basic"))),
		Login:            os.Getenv("RXMCP_LOGIN"),
		Password:         os.Getenv("RXMCP_PASSWORD"),
		Token:            os.Getenv("RXMCP_TOKEN"),
		Cookie:           os.Getenv("RXMCP_COOKIE"),
		OIDCIssuer:       strings.TrimRight(os.Getenv("RXMCP_OIDC_ISSUER"), "/"),
		OIDCClientID:     os.Getenv("RXMCP_OIDC_CLIENT_ID"),
		OIDCClientSecret: os.Getenv("RXMCP_OIDC_CLIENT_SECRET"),
		OIDCScope:        os.Getenv("RXMCP_OIDC_SCOPE"),
		AllowWrite:       isTrue(os.Getenv("RXMCP_ALLOW_WRITE")),
		InsecureTLS:      isTrue(os.Getenv("RXMCP_INSECURE_TLS")),
		CAFile:           os.Getenv("RXMCP_CA"),
		Timeout:          30 * time.Second,
		PageSize:         20,
		MaxPageSize:      100,
		MaxTextChars:     20000,
		TimeZone:         os.Getenv("RXMCP_TZ"),
		HTTPAddr:         os.Getenv("RXMCP_HTTP_ADDR"),
		HTTPSecret:       os.Getenv("RXMCP_HTTP_SECRET"),
	}
	if v := os.Getenv("RXMCP_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("RXMCP_TIMEOUT: %w", err)
		}
		c.Timeout = d
	}
	if v := os.Getenv("RXMCP_OIDC_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("RXMCP_OIDC_PORT: %w", err)
		}
		c.OIDCPort = n
	}
	if v := os.Getenv("RXMCP_USER_ID"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("RXMCP_USER_ID: %w", err)
		}
		c.UserID = n
	}
	if v := os.Getenv("RXMCP_MAX_TEXT"); v != "" {
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
		return errors.New("не задан RXMCP_URL (адрес сервиса интеграции, например https://rx.company.ru/Integration)")
	}
	if !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://") {
		return errors.New("RXMCP_URL должен начинаться с http:// или https://")
	}
	switch c.Auth {
	case "basic", "header":
		if c.Login == "" || c.Password == "" {
			return errors.New("не заданы RXMCP_LOGIN и RXMCP_PASSWORD")
		}
	case "bearer":
		if c.Token == "" {
			return errors.New("RXMCP_AUTH=bearer требует RXMCP_TOKEN")
		}
	case "cookie":
		if c.Cookie == "" {
			return errors.New("RXMCP_AUTH=cookie требует RXMCP_COOKIE (значение заголовка Cookie из браузера после входа в RX)")
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

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func isTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on", "да":
		return true
	}
	return false
}
