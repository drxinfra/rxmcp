package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/drxinfra/rxmcp/internal/auth"
	"github.com/drxinfra/rxmcp/internal/config"
	"github.com/drxinfra/rxmcp/internal/secret"
	"github.com/drxinfra/rxmcp/internal/weblogin"
)

func TestSecretsMigrationRoundTrip(t *testing.T) {
	keyring.MockInit()
	secret.ResetCache()
	home := t.TempDir()
	t.Setenv("RXMCP_HOME", home)
	for _, k := range []string{"RXMCP_SECRETS", "RXMCP_URL", "RXMCP_PASSWORD", "RXMCP_OIDC_ISSUER"} {
		t.Setenv(k, "")
	}
	const rxURL = "https://rx.example/Integration"
	if err := config.SaveFile(map[string]string{"RXMCP_URL": rxURL, "RXMCP_LOGIN": "ivanov", "RXMCP_AUTH": "cookie", "RXMCP_PASSWORD": "p@ss"}); err != nil {
		t.Fatal(err)
	}
	cookie := "sungero_client=" + strings.Repeat("x", 4000)
	if err := auth.SaveCookie(rxURL, cookie); err != nil {
		t.Fatal(err)
	}
	sso := weblogin.File(home, rxURL)
	if err := weblogin.Save(sso, &weblogin.State{URL: "https://sso.example/realms/r/", Cookies: "KEYCLOAK_IDENTITY=abc"}); err != nil {
		t.Fatal(err)
	}

	if err := secretsToKeyring(false); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{auth.CookieFile(rxURL), sso} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s остался файлом после переноса", filepath.Base(f))
		}
	}
	b, _ := os.ReadFile(config.FilePath())
	if strings.Contains(string(b), "p@ss") || !strings.Contains(string(b), `"RXMCP_SECRETS": "keyring"`) {
		t.Fatalf("профиль после переноса:\n%s", b)
	}
	secret.ResetCache()
	if c, err := auth.LoadCookie(rxURL); err != nil || c != cookie {
		t.Fatalf("кука из связки ключей: %v, %d символов", err, len(c))
	}
	if st, err := weblogin.Load(sso); err != nil || st.Cookies != "KEYCLOAK_IDENTITY=abc" {
		t.Fatalf("куки провайдера из связки ключей: %v", err)
	}
	if p, _ := config.LoadFile(); p["RXMCP_PASSWORD"] != "p@ss" {
		t.Fatal("пароль не читается из связки ключей")
	}

	if err := secretsToFile(); err != nil {
		t.Fatal(err)
	}
	secret.ResetCache()
	if c, err := os.ReadFile(auth.CookieFile(rxURL)); err != nil || strings.TrimSpace(string(c)) != cookie {
		t.Fatalf("кука не вернулась в файл: %v", err)
	}
	if _, err := os.Stat(sso); err != nil {
		t.Error("куки провайдера не вернулись в файл")
	}
	b, _ = os.ReadFile(config.FilePath())
	if !strings.Contains(string(b), "p@ss") || strings.Contains(string(b), "RXMCP_SECRETS") {
		t.Fatalf("профиль после возврата:\n%s", b)
	}
	for _, acc := range []string{config.SecretAccount("RXMCP_PASSWORD"), secret.Account(auth.CookieFile(rxURL)), secret.Account(sso)} {
		if _, err := keyring.Get(secret.Service, acc); err == nil {
			t.Errorf("в связке ключей осталась запись %s", acc)
		}
	}
}

func TestConfigSetRefusesSecretsMode(t *testing.T) {
	t.Setenv("RXMCP_HOME", t.TempDir())
	err := configCmd([]string{"set", "RXMCP_SECRETS=keyring"})
	if err == nil || !strings.Contains(err.Error(), "rxmcp secrets") {
		t.Fatalf("config set RXMCP_SECRETS должен отправлять к rxmcp secrets: %v", err)
	}
}
