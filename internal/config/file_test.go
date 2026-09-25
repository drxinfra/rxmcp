package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProfileReadWrite(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RXMCP_HOME", dir)
	if m, err := LoadFile(); err != nil || len(m) != 0 {
		t.Fatalf("пустой каталог: %v %v", m, err)
	}
	in := map[string]string{"RXMCP_URL": "https://rx.example/Integration", "RXMCP_LOGIN": "ivanov", "RXMCP_TZ": ""}
	if err := SaveFile(in); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("права %v, ожидались 0600: в профиле бывает пароль", fi.Mode().Perm())
	}
	out, err := LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	if out["RXMCP_URL"] != in["RXMCP_URL"] || out["RXMCP_LOGIN"] != "ivanov" {
		t.Fatalf("не то прочитали: %v", out)
	}
	if _, ok := out["RXMCP_TZ"]; ok {
		t.Fatal("пустые значения не должны сохраняться")
	}
}

func TestProfileFeedsConfigAndEnvWins(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RXMCP_HOME", dir)
	if err := SaveFile(map[string]string{
		"RXMCP_URL": "https://rx.example/Integration", "RXMCP_LOGIN": "ivanov",
		"RXMCP_PASSWORD": "s3cret", "RXMCP_ALLOW_WRITE": "1", "RXMCP_TZ": "Europe/Moscow",
	}); err != nil {
		t.Fatal(err)
	}
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("профиля должно хватать без окружения: %v", err)
	}
	if !c.AllowWrite || c.Login != "ivanov" || c.TimeZone != "Europe/Moscow" {
		t.Fatalf("профиль прочитан неверно: %+v", c)
	}
	t.Setenv("RXMCP_LOGIN", "petrov")
	c, err = FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.Login != "petrov" {
		t.Fatal("окружение должно перекрывать профиль")
	}
}

func TestBrokenProfileIsAnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RXMCP_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{не json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := FromEnv(); err == nil {
		t.Fatal("битый профиль должен ронять запуск, а не молча терять настройки")
	}
}
