package rx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecretPathRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("RXMCP_HOME", filepath.Join(home, "rxcfg"))
	os.MkdirAll(filepath.Join(home, "rxcfg"), 0o700)
	os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	cookie := filepath.Join(home, "rxcfg", "cookie-rx.txt")
	key := filepath.Join(home, ".ssh", "id_ed25519")
	ok := filepath.Join(home, "report.txt")
	for _, f := range []string{cookie, key, ok} {
		os.WriteFile(f, []byte("x"), 0o600)
	}
	// Символьная ссылка на cookie из безопасного места тоже не проходит.
	link := filepath.Join(home, "notes.txt")
	os.Symlink(cookie, link)
	for _, f := range []string{cookie, key, link} {
		if _, bad := secretPath(f); !bad {
			t.Errorf("%s должен быть отклонён", f)
		}
	}
	if _, bad := secretPath(ok); bad {
		t.Errorf("%s отклонён зря", ok)
	}
	if _, bad := secretPath(filepath.Join(home, ".sshfoo")); bad {
		t.Error(".sshfoo это не .ssh")
	}
}

func TestNormalizeResultByKind(t *testing.T) {
	for _, c := range []struct{ kind, in, want string }{
		{"SimpleAssignment", "", "Complete"},
		{"ReviewAssignment", "", "Accepted"},
		{"ReviewAssignment", "на доработку", "ForRework"},
		{"ApprovalAssignment", "", "Approved"},
		{"ApprovalAssignment", "на доработку", "ForRevision"},
		{"ApprovalAssignment", "с замечаниями", "WithSuggestions"},
		{"FreeApprovalAssignment", "согласовать", "Approved"},
		{"FreeApprovalAssignment", "не согласовано", "ForRework"},
		{"AcquaintanceAssignment", "ознакомлен", "Acquainted"},
		{"ReviewManagerAssignment", "", "Explored"},
		{"ApprovalCheckingAssignment", "accept", "Accept"},
		{"ApprovalAssignment", "approved", "Approved"},
		{"CustomAssignment", "принять", "Accepted"},
		{"CustomAssignment", "", ""},
		{"CustomAssignment", "MyResult", "MyResult"},
	} {
		if got := normalizeResult(c.kind, c.in); got != c.want {
			t.Errorf("%s %q: %q, ждали %q", c.kind, c.in, got, c.want)
		}
	}
}
