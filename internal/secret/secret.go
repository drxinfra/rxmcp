// Package secret хранит секреты rxmcp: куку, токены OIDC, куки провайдера входа и секретные
// ключи профиля. По умолчанию это файлы с правами 0600 в каталоге настроек, как раньше.
// С RXMCP_SECRETS=keyring те же данные лежат в связке ключей ОС: Keychain на macOS,
// диспетчер учётных данных на Windows, Secret Service (GNOME Keyring, KWallet) на Linux.
//
// Остальной код работает с секретом как с файлом по пути: ReadFile, WriteFile, Remove.
// Путь в режиме keyring служит именем записи, поэтому разные RXMCP_HOME и разные системы RX
// не смешиваются, а в «Связке ключей» видно, что где лежит.
package secret

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

const (
	// Service имя службы в связке ключей: все записи rxmcp под ним.
	Service = "rxmcp"
	// Режимы хранения.
	File    = "file"
	Keyring = "keyring"

	// Длинное значение (кука sungero_client, куки провайдера) режется на части:
	// Windows не принимает больше 2560 байт, macOS — команду длиннее 4 КБ, а значение
	// там ещё и уходит в base64. 1500 байт с запасом проходят везде.
	chunk       = 1500
	partsPrefix = "rxmcp-parts:"

	// Кука читается на каждом запросе к RX. Чтение из Keychain — это запуск
	// /usr/bin/security (десятки миллисекунд), поэтому значение держится в памяти
	// пару секунд. Куку, обновлённую соседним процессом, сервер увидит с этой задержкой.
	cacheTTL = 2 * time.Second
)

// ModeFor режим хранения для каталога настроек: RXMCP_SECRETS из окружения,
// иначе из профиля config.json в этом каталоге, иначе file.
func ModeFor(dir string) string {
	if v := strings.TrimSpace(os.Getenv("RXMCP_SECRETS")); v != "" {
		return Normalize(v)
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return File
	}
	raw := map[string]any{}
	if json.Unmarshal(b, &raw) != nil {
		return File
	}
	for k, v := range raw {
		key := strings.ToUpper(strings.TrimSpace(k))
		if key == "RXMCP_SECRETS" || key == "SECRETS" {
			if s, ok := v.(string); ok {
				return Normalize(s)
			}
		}
	}
	return File
}

// Normalize приводит значение RXMCP_SECRETS к file или keyring; неизвестное
// значение возвращается как есть, чтобы Validate мог на него пожаловаться.
func Normalize(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "file", "files", "файл":
		return File
	case "keyring", "keychain", "os":
		return Keyring
	}
	return strings.ToLower(strings.TrimSpace(v))
}

func keyringMode(path string) bool { return ModeFor(filepath.Dir(path)) == Keyring }

// Account имя записи секрета-файла в связке ключей: полный путь.
func Account(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return filepath.Clean(path)
}

// ReadFile читает секрет. Если его нет, ошибка удовлетворяет errors.Is(err, fs.ErrNotExist).
func ReadFile(path string) ([]byte, error) {
	if !keyringMode(path) {
		return os.ReadFile(path)
	}
	v, err := Get(Account(path))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Режим включили, а секрет ещё лежит файлом (RXMCP_SECRETS задали в окружении
			// без rxmcp secrets keyring): читаем файл, при следующей записи он переедет.
			if b, ferr := os.ReadFile(path); ferr == nil {
				return b, nil
			}
			return nil, &fs.PathError{Op: "read", Path: path + " (связка ключей)", Err: fs.ErrNotExist}
		}
		return nil, err
	}
	return []byte(v), nil
}

// WriteFile сохраняет секрет. Файл пишется через временный и переименование с правами 0600:
// его читают сразу несколько процессов (Claude, Cursor), и читающий не должен застать его пустым.
func WriteFile(path string, data []byte) error {
	if keyringMode(path) {
		if err := Set(Account(path), string(data)); err != nil {
			return err
		}
		// Файл от прежнего режима больше не нужен и не должен лежать рядом.
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeFileAtomic(path, data)
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".rxmcp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	// os.CreateTemp и так создаёт файл с правами 0600.
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Remove удаляет секрет и в связке ключей, и в файле: после смены режима мог остаться любой.
// Отсутствие секрета не ошибка.
func Remove(path string) error {
	var errs []error
	if keyringMode(path) {
		if err := Delete(Account(path)); err != nil {
			errs = append(errs, err)
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Exists сообщает, сохранён ли секрет.
func Exists(path string) bool {
	_, err := ReadFile(path)
	return err == nil
}

// Where коротко говорит, где лежит секрет: путь к файлу или «связка ключей».
func Where(path string) string {
	if keyringMode(path) {
		return "связка ключей ОС (" + Service + ", " + Account(path) + ")"
	}
	return path
}

// --- связка ключей ---

type cached struct {
	val string
	at  time.Time
}

var (
	mu    sync.Mutex
	cache = map[string]cached{}
)

// Get читает запись из связки ключей. Нет записи — ошибка с fs.ErrNotExist.
func Get(acc string) (string, error) {
	mu.Lock()
	if c, ok := cache[acc]; ok && time.Since(c.at) < cacheTTL {
		mu.Unlock()
		return c.val, nil
	}
	mu.Unlock()
	v, err := get(acc)
	if err != nil {
		return "", err
	}
	mu.Lock()
	cache[acc] = cached{v, time.Now()}
	mu.Unlock()
	return v, nil
}

func get(acc string) (string, error) {
	head, err := keyring.Get(Service, acc)
	if err != nil {
		return "", wrap(err)
	}
	if !strings.HasPrefix(head, partsPrefix) {
		return head, nil
	}
	n, err := strconv.Atoi(strings.TrimPrefix(head, partsPrefix))
	if err != nil || n < 1 || n > 1000 {
		return "", fmt.Errorf("связка ключей: запись %s повреждена", acc)
	}
	var b strings.Builder
	for i := 1; i <= n; i++ {
		p, err := keyring.Get(Service, partName(acc, i))
		if err != nil {
			return "", fmt.Errorf("связка ключей: нет части %d из %d записи %s: %w", i, n, acc, wrap(err))
		}
		b.WriteString(p)
	}
	return b.String(), nil
}

// Set кладёт запись в связку ключей, при необходимости частями.
func Set(acc, val string) error {
	old := 0
	if head, err := keyring.Get(Service, acc); err == nil && strings.HasPrefix(head, partsPrefix) {
		old, _ = strconv.Atoi(strings.TrimPrefix(head, partsPrefix))
	}
	parts := split(val)
	if len(parts) == 1 {
		if err := keyring.Set(Service, acc, val); err != nil {
			return wrap(err)
		}
	} else {
		// Сначала части, потом заголовок: читающий процесс не увидит заголовок без частей.
		for i, p := range parts {
			if err := keyring.Set(Service, partName(acc, i+1), p); err != nil {
				return wrap(err)
			}
		}
		if err := keyring.Set(Service, acc, partsPrefix+strconv.Itoa(len(parts))); err != nil {
			return wrap(err)
		}
	}
	// Лишние части от прежнего, более длинного значения.
	from := len(parts) + 1
	if len(parts) == 1 {
		from = 1
	}
	for i := from; i <= old; i++ {
		_ = keyring.Delete(Service, partName(acc, i))
	}
	mu.Lock()
	cache[acc] = cached{val, time.Now()}
	mu.Unlock()
	return nil
}

// Delete удаляет запись и её части. Нет записи — не ошибка.
func Delete(acc string) error {
	mu.Lock()
	delete(cache, acc)
	mu.Unlock()
	head, err := keyring.Get(Service, acc)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return wrap(err)
	}
	if strings.HasPrefix(head, partsPrefix) {
		n, _ := strconv.Atoi(strings.TrimPrefix(head, partsPrefix))
		for i := 1; i <= n; i++ {
			_ = keyring.Delete(Service, partName(acc, i))
		}
	}
	if err := keyring.Delete(Service, acc); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return wrap(err)
	}
	return nil
}

// Check проверяет, что связка ключей доступна: пишет, читает и удаляет пробную запись.
// На сервере Linux без графического сеанса Secret Service нет, и это видно сразу.
func Check() error {
	acc := "rxmcp-probe"
	if err := keyring.Set(Service, acc, "ok"); err != nil {
		return wrap(err)
	}
	v, err := keyring.Get(Service, acc)
	_ = keyring.Delete(Service, acc)
	if err != nil {
		return wrap(err)
	}
	if v != "ok" {
		return errors.New("связка ключей вернула не то, что в неё положили")
	}
	return nil
}

// ResetCache забывает прочитанные значения (для тестов и после смены режима).
func ResetCache() {
	mu.Lock()
	cache = map[string]cached{}
	mu.Unlock()
}

func partName(acc string, i int) string { return acc + "#" + strconv.Itoa(i) }

func split(val string) []string {
	if len(val) <= chunk {
		return []string{val}
	}
	var out []string
	for len(val) > chunk {
		cut := chunk
		// Не резать посреди символа UTF-8: часть должна оставаться строкой.
		for cut > 0 && val[cut]&0xC0 == 0x80 {
			cut--
		}
		out = append(out, val[:cut])
		val = val[cut:]
	}
	if val != "" {
		out = append(out, val)
	}
	return out
}

func wrap(err error) error {
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		return fmt.Errorf("связка ключей: %w", fs.ErrNotExist)
	case errors.Is(err, keyring.ErrUnsupportedPlatform):
		return errors.New("связка ключей на этой системе не поддерживается: оставьте RXMCP_SECRETS=file")
	case errors.Is(err, keyring.ErrSetDataTooBig):
		return errors.New("связка ключей не приняла значение: слишком длинное")
	}
	return fmt.Errorf("связка ключей недоступна (%v); на сервере без графического сеанса оставьте RXMCP_SECRETS=file", err)
}
