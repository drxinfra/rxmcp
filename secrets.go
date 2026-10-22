package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/drxinfra/rxmcp/internal/auth"
	"github.com/drxinfra/rxmcp/internal/config"
	"github.com/drxinfra/rxmcp/internal/secret"
	"github.com/drxinfra/rxmcp/internal/weblogin"
)

// secretsCmd показывает, где лежат секреты, и переносит их между файлами и связкой ключей ОС.
//
//	rxmcp secrets          где что лежит и доступна ли связка ключей
//	rxmcp secrets keyring  перенести в связку ключей (Keychain, диспетчер учётных данных, Secret Service)
//	rxmcp secrets file     вернуть в файлы 0600
func secretsCmd(args []string) error {
	if len(args) == 0 {
		return secretsStatus()
	}
	if len(args) > 1 {
		return errors.New("rxmcp secrets [keyring | file]")
	}
	if os.Getenv("RXMCP_SECRETS") != "" {
		return errors.New("RXMCP_SECRETS задан в окружении и перекрывает профиль: уберите его и повторите")
	}
	switch secret.Normalize(args[0]) {
	case secret.Keyring:
		return secretsToKeyring(true)
	case secret.File:
		return secretsToFile()
	}
	return fmt.Errorf("rxmcp secrets: неизвестный режим %q (есть keyring и file)", args[0])
}

func keyringName() string {
	switch runtime.GOOS {
	case "darwin":
		return "Keychain"
	case "windows":
		return "диспетчер учётных данных Windows"
	}
	return "Secret Service (GNOME Keyring, KWallet)"
}

// secretPaths секреты-файлы текущего профиля: кука RX, куки провайдера, токены OIDC.
// Плюс любые такие файлы в каталоге настроек: после смены адреса могли остаться старые.
func secretPaths() []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	if cfg, err := config.FromEnv(); err == nil && cfg.URL != "" {
		add(auth.CookieFile(cfg.URL))
		add(weblogin.File(auth.Dir(), cfg.URL))
		if cfg.OIDCIssuer != "" {
			add(auth.DefaultCacheFile(cfg.OIDCIssuer, cfg.OIDCClientID))
		}
	}
	for _, pat := range []string{"cookie-*.txt", "sso-*.json", "tokens-*.json"} {
		m, _ := filepath.Glob(filepath.Join(auth.Dir(), pat))
		sort.Strings(m)
		for _, p := range m {
			add(p)
		}
	}
	return out
}

func secretsStatus() error {
	mode := secret.ModeFor(auth.Dir())
	fmt.Println("Каталог настроек:", auth.Dir())
	if mode == secret.Keyring {
		fmt.Println("Секреты: связка ключей ОС,", keyringName())
	} else {
		fmt.Println("Секреты: файлы с правами 0600")
	}
	if os.Getenv("RXMCP_SECRETS") != "" {
		fmt.Println("  (режим задан переменной окружения RXMCP_SECRETS)")
	}
	prof, err := config.LoadFile()
	if err != nil {
		return err
	}
	for _, k := range config.SecretKeys {
		if prof[k] != "" {
			fmt.Printf("  %-26s задано\n", k)
		}
	}
	for _, p := range secretPaths() {
		if secret.Exists(p) {
			fmt.Printf("  %-26s %s\n", filepath.Base(p), secret.Where(p))
		}
	}
	if mode == secret.File {
		if err := secret.Check(); err != nil {
			fmt.Println("Связка ключей:", err)
		} else {
			fmt.Printf("Связка ключей доступна (%s). Перенести туда: rxmcp secrets keyring\n", keyringName())
		}
	}
	return nil
}

// secretsToKeyring переносит секреты из файлов в связку ключей. Порядок такой, чтобы сбой
// на любом шаге ничего не терял: сначала копия в связку и сверка, потом смена режима
// в профиле, и только потом удаление файлов.
func secretsToKeyring(verbose bool) error {
	if secret.ModeFor(auth.Dir()) == secret.Keyring {
		if verbose {
			fmt.Println("Секреты уже в связке ключей. Посмотреть: rxmcp secrets")
		}
		return nil
	}
	if err := secret.Check(); err != nil {
		return err
	}
	prof, err := config.LoadFile()
	if err != nil {
		return err
	}
	var moved []string
	for _, p := range secretPaths() {
		b, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		acc := secret.Account(p)
		if err := secret.Set(acc, string(b)); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		secret.ResetCache()
		if v, err := secret.Get(acc); err != nil || v != string(b) {
			return fmt.Errorf("%s: в связке ключей не то, что записали; файлы не тронуты", filepath.Base(p))
		}
		moved = append(moved, p)
	}
	prof["RXMCP_SECRETS"] = secret.Keyring
	if err := config.SaveFile(prof); err != nil {
		return err
	}
	for _, p := range moved {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if verbose {
		n := len(moved)
		for _, k := range config.SecretKeys {
			if prof[k] != "" {
				n++
			}
		}
		fmt.Printf("Секреты перенесены в %s (записей: %d), в каталоге настроек их больше нет.\n", keyringName(), n)
		fmt.Println("Перезапускать клиента не нужно. Вернуть в файлы: rxmcp secrets file")
	}
	return nil
}

// secretsToFile возвращает секреты в файлы и удаляет их из связки ключей.
func secretsToFile() error {
	if secret.ModeFor(auth.Dir()) != secret.Keyring {
		fmt.Println("Секреты и так в файлах. Посмотреть: rxmcp secrets")
		return nil
	}
	prof, err := config.LoadFile()
	if err != nil {
		return err
	}
	data := map[string][]byte{}
	for _, p := range secretPaths() {
		v, err := secret.Get(secret.Account(p))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		data[p] = []byte(v)
	}
	delete(prof, "RXMCP_SECRETS")
	if err := config.SaveFile(prof); err != nil {
		return err
	}
	for p, b := range data {
		if err := secret.WriteFile(p, b); err != nil {
			return err
		}
	}
	for _, k := range config.SecretKeys {
		_ = secret.Delete(config.SecretAccount(k))
	}
	for p := range data {
		_ = secret.Delete(secret.Account(p))
	}
	fmt.Println("Секреты возвращены в файлы с правами 0600 в", auth.Dir())
	return nil
}
