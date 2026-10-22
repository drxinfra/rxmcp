package config

import (
	"os"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/drxinfra/rxmcp/internal/secret"
)

func TestProfileSecretsInKeyring(t *testing.T) {
	keyring.MockInit()
	secret.ResetCache()
	t.Setenv("RXMCP_HOME", t.TempDir())
	t.Setenv("RXMCP_SECRETS", "")
	prof := map[string]string{"RXMCP_URL": "https://rx.example", "RXMCP_PASSWORD": "p@ss", "RXMCP_SECRETS": "keyring"}
	if err := SaveFile(prof); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(FilePath())
	if strings.Contains(string(b), "p@ss") || strings.Contains(string(b), "RXMCP_PASSWORD") {
		t.Fatalf("пароль остался в файле профиля:\n%s", b)
	}
	got, err := LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	if got["RXMCP_PASSWORD"] != "p@ss" || got["RXMCP_URL"] != "https://rx.example" {
		t.Fatalf("профиль прочитан не так: %v", got)
	}
	delete(got, "RXMCP_PASSWORD")
	if err := SaveFile(got); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get(secret.Service, SecretAccount("RXMCP_PASSWORD")); err == nil {
		t.Error("unset не удалил пароль из связки ключей")
	}
}

func TestSecretsModeValidate(t *testing.T) {
	c := &Config{Secrets: "vault", URL: "https://rx.example", Auth: "basic", Login: "a", Password: "b", HTTPAuth: "secret"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "RXMCP_SECRETS") {
		t.Fatalf("неизвестный режим секретов принят: %v", err)
	}
}
