package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/drxinfra/rxmcp/internal/auth"
	"github.com/drxinfra/rxmcp/internal/config"
	"github.com/drxinfra/rxmcp/internal/update"
)

// availableUpdate новая версия, если вышла и проверка не выключена; иначе пусто.
// Ошибки сети молча проглатываются: без интернета rxmcp работает как раньше.
func availableUpdate(timeout time.Duration) string {
	if cfg, err := config.FromEnv(); err == nil && !cfg.UpdateCheck {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := &update.Checker{Current: version, Dir: auth.Dir()}
	v, _ := c.Available(ctx)
	return v
}

// selfUpdate ставит последний релиз вместо текущего бинарника.
// Работает и при RXMCP_UPDATE_CHECK=0: человек попросил явно.
func selfUpdate() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := &update.Checker{Current: version, Dir: auth.Dir(), HTTP: &http.Client{Timeout: 3 * time.Minute}}
	latest, err := c.Latest(ctx)
	if err != nil {
		return err
	}
	if update.Release(version) && !update.Newer(version, latest) {
		fmt.Printf("У вас последняя версия: %s\n", version)
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	fmt.Printf("Скачиваю %s (сейчас %s)\n", latest, version)
	bin, from, err := c.Download(ctx, latest)
	if err != nil {
		return fmt.Errorf("не удалось скачать %s: %w", latest, err)
	}
	if err := update.Replace(exe, bin); err != nil {
		return err
	}
	fmt.Printf("Обновлено: %s → %s (%s, сумма сверена с SHA256SUMS)\n", version, latest, from)
	fmt.Println("Перезапустите ИИ-клиент, чтобы он запустил новую версию сервера.")
	return nil
}
