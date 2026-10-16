// Package update узнаёт, вышла ли новая версия rxmcp, и ставит её вместо текущего бинарника.
//
// Это единственное соединение rxmcp не к RX. В запросе только адрес файла с номером версии:
// ни адреса RX, ни логина, ни других данных пользователя. Выключается RXMCP_UPDATE_CHECK=0.
package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	// Mirror открывается из сетей, где GitHub закрыт; его же спрашивает install.sh.
	Mirror = "https://drxinfra.ru/dl/rxmcp"
	GitHub = "https://github.com/drxinfra/rxmcp"
	// Every как часто спрашивать: чаще раза в сутки незачем.
	Every = 24 * time.Hour
)

var semver = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// Release версия вида v1.2.3. Сборки из git (v0.8.0-3-gabc, dev) релизом не считаются.
func Release(v string) bool { return semver.MatchString(v) }

// Newer сообщает, что b новее a. Оба должны быть релизами.
func Newer(a, b string) bool {
	pa, pb := semver.FindStringSubmatch(a), semver.FindStringSubmatch(b)
	if pa == nil || pb == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return y > x
		}
	}
	return false
}

// Checker узнаёт последнюю версию и помнит ответ сутки в каталоге настроек.
type Checker struct {
	Current string
	Dir     string // каталог настроек; пусто = без кэша
	HTTP    *http.Client
	Mirror  string // для тестов; пусто = Mirror
	GitHub  string // для тестов; пусто = GitHub
	Now     func() time.Time
}

type state struct {
	Checked time.Time `json:"checked"`
	Latest  string    `json:"latest"`
}

func (c *Checker) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 5 * time.Second}
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Checker) mirror() string { return strings.TrimRight(firstNonEmpty(c.Mirror, Mirror), "/") }
func (c *Checker) github() string { return strings.TrimRight(firstNonEmpty(c.GitHub, GitHub), "/") }

func (c *Checker) file() string { return filepath.Join(c.Dir, "update-check.json") }

// Available возвращает новую версию, если она вышла, иначе пустую строку.
// Ответ берётся из кэша, если ему меньше суток. Ошибки сети не мешают работе: пусто и ошибка.
func (c *Checker) Available(ctx context.Context) (string, error) {
	if !Release(c.Current) {
		return "", nil
	}
	if st, ok := c.load(); ok && c.now().Sub(st.Checked) < Every {
		if Newer(c.Current, st.Latest) {
			return st.Latest, nil
		}
		return "", nil
	}
	latest, err := c.Latest(ctx)
	if err != nil {
		return "", err
	}
	c.save(state{Checked: c.now(), Latest: latest})
	if Newer(c.Current, latest) {
		return latest, nil
	}
	return "", nil
}

// Latest спрашивает последнюю версию: сначала зеркало, потом GitHub.
func (c *Checker) Latest(ctx context.Context) (string, error) {
	v, errM := c.fromMirror(ctx)
	if errM == nil {
		return v, nil
	}
	v, errG := c.fromGitHub(ctx)
	if errG == nil {
		return v, nil
	}
	return "", fmt.Errorf("не удалось узнать последнюю версию: зеркало: %v; GitHub: %v", errM, errG)
}

func (c *Checker) fromMirror(ctx context.Context) (string, error) {
	body, err := c.get(ctx, c.mirror()+"/latest.txt", 64)
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(body))
	if !Release(v) {
		return "", fmt.Errorf("latest.txt: %q не похоже на версию", v)
	}
	return v, nil
}

// fromGitHub узнаёт версию по редиректу /releases/latest: без токена и без лимитов API.
func (c *Checker) fromGitHub(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.github()+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	cl := *c.client()
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := cl.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	i := strings.LastIndex(loc, "/tag/")
	if i < 0 {
		return "", fmt.Errorf("GitHub ответил %d без ссылки на релиз", resp.StatusCode)
	}
	v := loc[i+len("/tag/"):]
	if !Release(v) {
		return "", fmt.Errorf("GitHub: %q не похоже на версию", v)
	}
	return v, nil
}

func (c *Checker) get(ctx context.Context, url string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "rxmcp/"+c.Current)
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > max {
		return nil, fmt.Errorf("%s: ответ больше %d байт", url, max)
	}
	return body, nil
}

func (c *Checker) load() (state, bool) {
	if c.Dir == "" {
		return state{}, false
	}
	b, err := os.ReadFile(c.file())
	if err != nil {
		return state{}, false
	}
	var st state
	if json.Unmarshal(b, &st) != nil || !Release(st.Latest) {
		return state{}, false
	}
	return st, true
}

func (c *Checker) save(st state) {
	if c.Dir == "" {
		return
	}
	b, _ := json.Marshal(st)
	_ = os.MkdirAll(c.Dir, 0o700)
	_ = os.WriteFile(c.file(), b, 0o600)
}

// Archive имя архива релиза под эту платформу, как его называет hack/build.sh.
func Archive(version, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("rxmcp-%s-%s-%s%s", version, goos, goarch, ext)
}

// Download скачивает архив версии и сверяет его с SHA256SUMS того же источника.
// Источники по очереди: релиз на GitHub, зеркало. Возвращает бинарник из архива.
func (c *Checker) Download(ctx context.Context, version string) ([]byte, string, error) {
	name := Archive(version, runtime.GOOS, runtime.GOARCH)
	var errs []string
	for _, base := range []string{c.github() + "/releases/download/" + version, c.mirror()} {
		bin, err := c.downloadFrom(ctx, base, name)
		if err == nil {
			return bin, base, nil
		}
		errs = append(errs, err.Error())
	}
	return nil, "", errors.New(strings.Join(errs, "; "))
}

func (c *Checker) downloadFrom(ctx context.Context, base, name string) ([]byte, error) {
	sums, err := c.get(ctx, base+"/SHA256SUMS", 64<<10)
	if err != nil {
		return nil, err
	}
	want, ok := sumFor(sums, name)
	if !ok {
		return nil, fmt.Errorf("%s/SHA256SUMS: нет строки для %s", base, name)
	}
	arch, err := c.get(ctx, base+"/"+name, 200<<20)
	if err != nil {
		return nil, err
	}
	got := sha256.Sum256(arch)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("%s: контрольная сумма не совпала с SHA256SUMS", name)
	}
	return extract(arch, name)
}

func sumFor(sums []byte, name string) (string, bool) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return strings.ToLower(f[0]), true
		}
	}
	return "", false
}

// extract достаёт rxmcp (rxmcp.exe) из архива релиза.
func extract(arch []byte, name string) ([]byte, error) {
	if strings.HasSuffix(name, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(arch), int64(len(arch)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if filepath.Base(f.Name) == "rxmcp.exe" {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(rc)
			}
		}
		return nil, errors.New("в архиве нет rxmcp.exe")
	}
	gz, err := gzip.NewReader(bytes.NewReader(arch))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("в архиве нет rxmcp")
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == "rxmcp" {
			return io.ReadAll(tr)
		}
	}
}

// Replace ставит новый бинарник на место exe. Работающий процесс (сервер в клиенте)
// мешать не должен: файл сначала пишется рядом, старый уходит в сторону, новый
// переименовывается на его место. Windows не даёт удалить запущенный exe, поэтому
// старый остаётся как rxmcp.exe.old и удаляется при следующем обновлении.
func Replace(exe string, bin []byte) error {
	dir := filepath.Dir(exe)
	tmp := exe + ".new"
	_ = os.Remove(exe + ".old")
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("нет прав на запись в %s: запустите обновление с правами, которыми ставили rxmcp (например, sudo rxmcp update)", dir)
		}
		return err
	}
	if err := os.Rename(exe, exe+".old"); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Rename(exe+".old", exe)
		return err
	}
	if runtime.GOOS != "windows" {
		_ = os.Remove(exe + ".old")
	}
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
