package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeRX(t *testing.T) {
	cases := map[string]string{
		"https://rx.example":                             "https://rx.example/integration",
		"rx.example":                                     "https://rx.example/integration",
		"https://rx.example/":                            "https://rx.example/integration",
		"https://rx.example/Integration":                 "https://rx.example/Integration",
		"https://rx.example/Integration/odata":           "https://rx.example/Integration",
		"https://rx.example/integration/odata/$metadata": "https://rx.example/integration",
		"http://rx.example:8080/Integration":             "http://rx.example:8080/Integration",
	}
	for in, want := range cases {
		got, err := normalizeRX(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q -> %q, ожидалось %q", in, got, want)
		}
	}
	if _, err := normalizeRX("   "); err == nil {
		t.Error("пустой адрес должен быть ошибкой")
	}
}

func TestSecondProfileRegistration(t *testing.T) {
	t.Setenv("RXMCP_HOME", "")
	if n := clientName("https://rxc.test.example.com/Integration"); n != "rx" {
		t.Errorf("профиль по умолчанию: %s", n)
	}
	home := t.TempDir()
	t.Setenv("RXMCP_HOME", home)
	if n := clientName("https://rxc.test.example.com/Integration"); n != "rx-rxc-test" {
		t.Errorf("второй профиль: %s", n)
	}
	path := filepath.Join(t.TempDir(), "claude_desktop_config.json")
	os.WriteFile(path, []byte(`{"mcpServers":{"rx":{"command":"/usr/local/bin/rxmcp"}}}`), 0o600)
	if err := mergeMCPConfig(path, "rx-rxc-test", "/usr/local/bin/rxmcp", home); err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	b, _ := os.ReadFile(path)
	json.Unmarshal(b, &cfg)
	if cfg.MCPServers["rx"].Command == "" || cfg.MCPServers["rx"].Env != nil {
		t.Errorf("запись rx испорчена: %s", b)
	}
	if cfg.MCPServers["rx-rxc-test"].Env["RXMCP_HOME"] != home {
		t.Errorf("второй сервер без RXMCP_HOME: %s", b)
	}
}
