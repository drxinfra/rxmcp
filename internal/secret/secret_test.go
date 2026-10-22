package secret

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func home(t *testing.T, mode string) string {
	t.Helper()
	keyring.MockInit()
	ResetCache()
	dir := t.TempDir()
	t.Setenv("RXMCP_SECRETS", "")
	if mode != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"RXMCP_SECRETS": "`+mode+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestModeFor(t *testing.T) {
	dir := home(t, "")
	if m := ModeFor(dir); m != File {
		t.Fatalf("без профиля %q", m)
	}
	dir = home(t, "keychain")
	if m := ModeFor(dir); m != Keyring {
		t.Fatalf("keychain в профиле: %q", m)
	}
	t.Setenv("RXMCP_SECRETS", "file")
	if m := ModeFor(dir); m != File {
		t.Fatalf("окружение должно перекрывать профиль: %q", m)
	}
}

func TestFileMode(t *testing.T) {
	dir := home(t, "")
	p := filepath.Join(dir, "cookie-rx.txt")
	if err := WriteFile(p, []byte("a=1")); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		t.Errorf("права файла %v, нужно 0600", st.Mode().Perm())
	}
	if _, err := keyring.Get(Service, Account(p)); err == nil {
		t.Error("в режиме file запись попала в связку ключей")
	}
	if err := Remove(p); err != nil || Exists(p) {
		t.Fatalf("Remove: %v", err)
	}
	if err := Remove(p); err != nil {
		t.Errorf("повторный Remove: %v", err)
	}
}

func TestKeyringModeLongValue(t *testing.T) {
	dir := home(t, "keyring")
	p := filepath.Join(dir, "cookie-rx.txt")
	// Старый файл от режима file: после записи в связку его быть не должно.
	os.WriteFile(p, []byte("old"), 0o600)
	long := "sungero_client=" + strings.Repeat("Ab0+/ж", 1200) // больше лимита Windows в 2560 байт
	if err := WriteFile(p, []byte(long)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Error("файл остался рядом со связкой ключей")
	}
	head, _ := keyring.Get(Service, Account(p))
	if !strings.HasPrefix(head, partsPrefix) {
		t.Fatalf("длинное значение не разрезано: %.40q", head)
	}
	ResetCache()
	b, err := ReadFile(p)
	if err != nil || string(b) != long {
		t.Fatalf("прочитано не то: %v, %d байт из %d", err, len(b), len(long))
	}
	// Короткое значение поверх длинного: части должны уйти.
	if err := WriteFile(p, []byte("a=1")); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get(Service, partName(Account(p), 1)); err == nil {
		t.Error("остались части прежнего значения")
	}
	ResetCache()
	if b, _ := ReadFile(p); string(b) != "a=1" {
		t.Fatalf("после перезаписи %q", b)
	}
	if err := Remove(p); err != nil {
		t.Fatal(err)
	}
	ResetCache()
	if _, err := ReadFile(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("после Remove: %v", err)
	}
}

func TestKeyringModeReadsLeftoverFile(t *testing.T) {
	dir := home(t, "keyring")
	p := filepath.Join(dir, "tokens-sso-rx.json")
	os.WriteFile(p, []byte(`{"x":1}`), 0o600)
	b, err := ReadFile(p)
	if err != nil || string(b) != `{"x":1}` {
		t.Fatalf("файл, оставшийся от режима file, не прочитан: %v %q", err, b)
	}
}

func TestSplitKeepsUTF8(t *testing.T) {
	s := strings.Repeat("ж", 2000)
	parts := split(s)
	if len(parts) < 2 {
		t.Fatal("не разрезано")
	}
	for i, p := range parts {
		if len(p) > chunk {
			t.Errorf("часть %d длиннее %d", i, chunk)
		}
		if !strings.HasPrefix(p, "ж") {
			t.Errorf("часть %d начинается посреди символа", i)
		}
	}
	if strings.Join(parts, "") != s {
		t.Error("части не складываются в исходное")
	}
}
