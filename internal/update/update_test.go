package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"v0.8.0", "v0.9.0", true},
		{"v0.8.0", "v0.8.1", true},
		{"v0.9.0", "v0.10.0", true}, // числа, а не строки
		{"v0.8.0", "v0.8.0", false},
		{"v1.0.0", "v0.9.9", false},
		{"dev", "v0.9.0", false},
		{"v0.8.0-3-gabc", "v0.9.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

func TestAvailableMirrorThenGitHubAndCache(t *testing.T) {
	mirrorHits, ghHits := 0, 0
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mirrorHits++
		w.WriteHeader(503)
	}))
	defer mirror.Close()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ghHits++
		http.Redirect(w, r, "/drxinfra/rxmcp/releases/tag/v0.9.0", http.StatusFound)
	}))
	defer gh.Close()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	c := &Checker{Current: "v0.8.0", Dir: t.TempDir(), Mirror: mirror.URL, GitHub: gh.URL, Now: func() time.Time { return now }}
	v, err := c.Available(context.Background())
	if err != nil || v != "v0.9.0" {
		t.Fatalf("Available = %q, %v", v, err)
	}
	// В течение суток сеть не трогаем.
	now = now.Add(23 * time.Hour)
	if v, _ := c.Available(context.Background()); v != "v0.9.0" || mirrorHits != 1 || ghHits != 1 {
		t.Errorf("кэш не сработал: %q, зеркало %d, GitHub %d", v, mirrorHits, ghHits)
	}
	now = now.Add(2 * time.Hour)
	c.Available(context.Background())
	if mirrorHits != 2 {
		t.Errorf("через сутки должна быть новая проверка, зеркало %d", mirrorHits)
	}
	// Сборка из исходников не спрашивает вовсе.
	d := &Checker{Current: "dev", Mirror: mirror.URL, GitHub: gh.URL}
	if v, _ := d.Available(context.Background()); v != "" || mirrorHits != 2 {
		t.Errorf("dev: %q, зеркало %d", v, mirrorHits)
	}
}

func tgz(t *testing.T, name string, body []byte) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "rxmcp-v0.9.0/" + name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write(body)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestDownloadChecksum(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("архив tar.gz")
	}
	name := Archive("v0.9.0", runtime.GOOS, runtime.GOARCH)
	arch := tgz(t, "rxmcp", []byte("new binary"))
	sum := sha256.Sum256(arch)
	good := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name)
	bad := fmt.Sprintf("%s  %s\n", hex.EncodeToString(make([]byte, 32)), name)
	serve := func(sums string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch filepath.Base(r.URL.Path) {
			case "SHA256SUMS":
				fmt.Fprint(w, sums)
			case name:
				w.Write(arch)
			default:
				w.WriteHeader(404)
			}
		}))
	}
	// GitHub отдаёт подменённый архив, зеркало честное: берём с зеркала.
	gh, mirror := serve(bad), serve(good)
	defer gh.Close()
	defer mirror.Close()
	c := &Checker{Current: "v0.8.0", GitHub: gh.URL, Mirror: mirror.URL}
	bin, from, err := c.Download(context.Background(), "v0.9.0")
	if err != nil || string(bin) != "new binary" || from != mirror.URL {
		t.Fatalf("Download = %q from %s, %v", bin, from, err)
	}
	// Оба источника с неверной суммой: ничего не ставим.
	c.Mirror = gh.URL
	if _, _, err := c.Download(context.Background(), "v0.9.0"); err == nil {
		t.Error("неверная сумма должна отклоняться")
	}
}

func TestReplace(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "rxmcp")
	os.WriteFile(exe, []byte("old"), 0o755)
	if err := Replace(exe, []byte("new")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(exe)
	st, _ := os.Stat(exe)
	if string(b) != "new" || st.Mode().Perm()&0o100 == 0 {
		t.Errorf("после замены %q, права %v", b, st.Mode())
	}
}
