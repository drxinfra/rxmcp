package auth

import (
	"os"
	"path/filepath"
	"runtime"
)

// Dir каталог настроек rxmcp. Один на все файлы: профиль, кука, токены OIDC.
// Переопределяется RXMCP_HOME — удобно для контейнера и для нескольких систем RX.
func Dir() string {
	if d := os.Getenv("RXMCP_HOME"); d != "" {
		return d
	}
	if runtime.GOOS == "windows" {
		if d, err := os.UserConfigDir(); err == nil {
			return filepath.Join(d, "rxmcp")
		}
		return filepath.Join(os.TempDir(), "rxmcp")
	}
	// На macOS os.UserConfigDir даёт ~/Library/Application Support: путь с пробелом,
	// его неудобно показывать в инструкциях. Держим ~/.config/rxmcp везде, кроме Windows.
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "rxmcp")
	}
	return filepath.Join(os.TempDir(), "rxmcp")
}

// EnsureDir создаёт каталог настроек с правами 0700.
func EnsureDir() (string, error) {
	d := Dir()
	return d, os.MkdirAll(d, 0o700)
}
