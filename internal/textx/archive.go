package textx

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Архивы во вложениях: логи в zip и gz, снимки в tar.gz. Содержимое архива целиком модели
// не нужно, а вот список файлов и текст одиночного .gz (лог) нужен.

// MaxUnpacked предел распакованного .gz: больше в контекст всё равно не войдёт.
const MaxUnpacked = 64 << 20

const maxListed = 200

// Unpack разбирает архив. Для zip (не docx/xlsx) и tar возвращает список файлов в listing.
// Для одиночного .gz возвращает распакованное тело и расширение файла внутри (по имени
// без .gz или по содержимому), чтобы дальше прочитать его как обычный файл.
// ok=false значит, что это не архив и data нужно читать как есть.
func Unpack(data []byte, ext, name string) (listing string, inner []byte, innerExt string, ok bool, err error) {
	switch {
	case ext == "zip" || (ext == "" && Sniff(data) == "zip"):
		if isOOXML(data) {
			return "", nil, "", false, nil
		}
		l, err := zipListing(data)
		return l, nil, "", true, err
	case ext == "tar":
		l, err := tarListing(bytes.NewReader(data))
		return l, nil, "", true, err
	case ext == "gz" || ext == "tgz" || bytes.HasPrefix(data, []byte{0x1f, 0x8b}):
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return "", nil, "", true, fmt.Errorf("gzip не распакован: %w", err)
		}
		// tar внутри читаем потоком: снимок логов в распакованном виде бывает
		// в десятки раз больше архива, а для списка держать его в памяти незачем.
		br := bufio.NewReaderSize(zr, 1024)
		if head, _ := br.Peek(512); isTar(head) {
			l, err := tarListing(br)
			return l, nil, "", true, err
		}
		body, err := io.ReadAll(io.LimitReader(br, MaxUnpacked+1))
		if err != nil {
			return "", nil, "", true, fmt.Errorf("gzip не распакован: %w", err)
		}
		if len(body) > MaxUnpacked {
			return "", nil, "", true, fmt.Errorf("внутри gzip больше %d МБ, не распаковываю", MaxUnpacked>>20)
		}
		base := strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(name), ".gz"), ".tgz")
		e := ""
		if i := strings.LastIndex(base, "."); i >= 0 && len(base)-i <= 6 {
			e = base[i+1:]
		}
		if e == "" {
			e = Sniff(body)
		}
		return "", body, e, true, nil
	}
	return "", nil, "", false, nil
}

func isTar(b []byte) bool {
	return len(b) >= 262 && bytes.Equal(b[257:262], []byte("ustar"))
}

func zipListing(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("zip не прочитан: %w", err)
	}
	var b strings.Builder
	n := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		n++
		if n <= maxListed {
			fmt.Fprintf(&b, "  %s · %s\n", f.Name, sizeText(int64(f.UncompressedSize64)))
		}
	}
	return listingHead("zip", n) + b.String() + listingTail(n), nil
}

func tarListing(r io.Reader) (string, error) {
	tr := tar.NewReader(r)
	var b strings.Builder
	n := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("tar не прочитан: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		n++
		if n <= maxListed {
			fmt.Fprintf(&b, "  %s · %s\n", h.Name, sizeText(h.Size))
		}
	}
	return listingHead("tar", n) + b.String() + listingTail(n), nil
}

func listingHead(kind string, n int) string {
	return fmt.Sprintf("Архив %s, файлов %d. Прочитать файл изнутри: тот же вызов с path = путь из списка.\n", kind, n)
}

func listingTail(n int) string {
	if n > maxListed {
		return fmt.Sprintf("  … и ещё %d\n", n-maxListed)
	}
	return ""
}

func sizeText(n int64) string {
	switch {
	case n >= 1<<20:
		return strings.Replace(fmt.Sprintf("%.1f МБ", float64(n)/(1<<20)), ".", ",", 1)
	case n >= 1<<10:
		return fmt.Sprintf("%d КБ", n>>10)
	}
	return fmt.Sprintf("%d Б", n)
}

// ArchiveFile достаёт из zip, tar или tar.gz один файл по пути из списка Unpack.
// Возвращает тело и расширение файла внутри.
func ArchiveFile(data []byte, ext, inner string) ([]byte, string, error) {
	inner = strings.TrimPrefix(strings.TrimSpace(inner), "./")
	extOf := func(name string) string {
		base := strings.ToLower(name)
		if i := strings.LastIndex(base, "."); i >= 0 && len(base)-i <= 6 && !strings.Contains(base[i:], "/") {
			return base[i+1:]
		}
		return ""
	}
	limited := func(r io.Reader, size int64) ([]byte, error) {
		if size > MaxUnpacked {
			return nil, fmt.Errorf("%s больше %d МБ, не читаю", inner, MaxUnpacked>>20)
		}
		return io.ReadAll(io.LimitReader(r, MaxUnpacked))
	}
	if ext == "zip" || bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, "", fmt.Errorf("zip не прочитан: %w", err)
		}
		for _, f := range zr.File {
			if f.Name != inner {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, "", err
			}
			defer rc.Close()
			b, err := limited(rc, int64(f.UncompressedSize64))
			return b, extOf(f.Name), err
		}
		return nil, "", fmt.Errorf("в архиве нет файла %q; список: вызов без path", inner)
	}
	var r io.Reader = bytes.NewReader(data)
	if bytes.HasPrefix(data, []byte{0x1f, 0x8b}) {
		zr, err := gzip.NewReader(r)
		if err != nil {
			return nil, "", fmt.Errorf("gzip не распакован: %w", err)
		}
		r = zr
	}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, "", fmt.Errorf("в архиве нет файла %q; список: вызов без path", inner)
		}
		if err != nil {
			return nil, "", fmt.Errorf("архив не прочитан: %w", err)
		}
		if h.Typeflag == tar.TypeReg && strings.TrimPrefix(h.Name, "./") == inner {
			b, err := limited(tr, h.Size)
			return b, extOf(h.Name), err
		}
	}
}
