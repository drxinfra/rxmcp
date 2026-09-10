// Package textx извлекает текст из тел документов без внешних библиотек.
package textx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// Extract возвращает текст и признак того, что формат поддержан.
// ext без точки, в нижнем регистре. Если ext пустой, формат угадывается по содержимому.
func Extract(data []byte, ext string) (string, error) {
	if len(data) == 0 {
		return "", errors.New("пустое тело")
	}
	if ext == "" {
		ext = sniff(data)
	}
	switch ext {
	case "docx", "docm", "dotx":
		return docx(data)
	case "xlsx", "xlsm":
		return xlsx(data)
	case "pptx":
		return pptx(data)
	case "txt", "md", "csv", "json", "xml", "yaml", "yml", "log", "ini", "sql":
		return plain(data), nil
	case "html", "htm":
		return stripTags(plain(data)), nil
	case "rtf":
		return rtf(plain(data)), nil
	case "pdf":
		return "", errors.New("pdf: извлечение текста в этой версии не поддерживается; попросите docx-версию или откройте документ в RX")
	case "doc", "xls", "ppt":
		return "", fmt.Errorf("%s: старый бинарный формат Office не поддерживается", ext)
	case "zip":
		if isOOXML(data) {
			return docx(data)
		}
	}
	if looksText(data) {
		return plain(data), nil
	}
	return "", fmt.Errorf("формат %q не поддерживается (умею docx, xlsx, pptx, txt, md, csv, json, xml, html, rtf)", ext)
}

func sniff(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return "zip"
	case bytes.HasPrefix(data, []byte("%PDF")):
		return "pdf"
	case bytes.HasPrefix(data, []byte("{\\rtf")):
		return "rtf"
	case bytes.HasPrefix(data, []byte("\xD0\xCF\x11\xE0")):
		return "doc"
	}
	return ""
}

func isOOXML(data []byte) bool {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return false
	}
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			return true
		}
	}
	return false
}

func readZip(data []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("не zip: %w", err)
	}
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, 64<<20))
		}
	}
	return nil, fmt.Errorf("в архиве нет %s", name)
}

// docx: параграфы из word/document.xml, таблицы через табуляцию.
func docx(data []byte) (string, error) {
	x, err := readZip(data, "word/document.xml")
	if err != nil {
		return "", err
	}
	return ooxmlText(x, map[string]string{"p": "\n", "tab": "\t", "br": "\n", "tc": "\t", "tr": "\n"}), nil
}

func pptx(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	var names []string
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
			names = append(names, f.Name)
		}
	}
	sort.Slice(names, func(i, j int) bool { return slideNum(names[i]) < slideNum(names[j]) })
	var b strings.Builder
	for i, n := range names {
		x, err := readZip(data, n)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "--- слайд %d ---\n", i+1)
		b.WriteString(ooxmlText(x, map[string]string{"p": "\n", "br": "\n"}))
		b.WriteString("\n")
	}
	return b.String(), nil
}

var slideRe = regexp.MustCompile(`slide(\d+)\.xml`)

func slideNum(n string) int {
	m := slideRe.FindStringSubmatch(n)
	if len(m) < 2 {
		return 0
	}
	v := 0
	fmt.Sscanf(m[1], "%d", &v)
	return v
}

// xlsx: строки листов, ячейки через табуляцию, общие строки из sharedStrings.
func xlsx(data []byte) (string, error) {
	shared := []string{}
	if x, err := readZip(data, "xl/sharedStrings.xml"); err == nil {
		d := xml.NewDecoder(bytes.NewReader(x))
		var cur strings.Builder
		in := false
		for {
			tok, err := d.Token()
			if err != nil {
				break
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "si" {
					cur.Reset()
					in = true
				}
			case xml.CharData:
				if in {
					cur.Write(t)
				}
			case xml.EndElement:
				if t.Name.Local == "si" {
					shared = append(shared, cur.String())
					in = false
				}
			}
		}
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	var sheets []string
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml") {
			sheets = append(sheets, f.Name)
		}
	}
	sort.Strings(sheets)
	var b strings.Builder
	for i, n := range sheets {
		x, err := readZip(data, n)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "--- лист %d ---\n", i+1)
		d := xml.NewDecoder(bytes.NewReader(x))
		var cellType string
		var val strings.Builder
		inV := false
		for {
			tok, err := d.Token()
			if err != nil {
				break
			}
			switch t := tok.(type) {
			case xml.StartElement:
				switch t.Name.Local {
				case "c":
					cellType = ""
					for _, a := range t.Attr {
						if a.Name.Local == "t" {
							cellType = a.Value
						}
					}
				case "v", "t":
					inV = true
					val.Reset()
				}
			case xml.CharData:
				if inV {
					val.Write(t)
				}
			case xml.EndElement:
				switch t.Name.Local {
				case "v", "t":
					inV = false
					s := val.String()
					if cellType == "s" {
						idx := 0
						if _, err := fmt.Sscanf(s, "%d", &idx); err == nil && idx < len(shared) {
							s = shared[idx]
						}
					}
					b.WriteString(s)
					b.WriteString("\t")
				case "row":
					b.WriteString("\n")
				}
			}
		}
	}
	return b.String(), nil
}

// ooxmlText собирает текст из XML: содержимое элементов t; параграфы через перевод строки,
// ячейки таблиц через табуляцию, строки таблиц через перевод строки.
func ooxmlText(x []byte, _ map[string]string) string {
	d := xml.NewDecoder(bytes.NewReader(x))
	var b strings.Builder
	inT, inCell := false, 0
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inT = true
			case "tab":
				b.WriteString("\t")
			case "br":
				b.WriteString("\n")
			case "tc":
				inCell++
			}
		case xml.CharData:
			if inT {
				b.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "p":
				if inCell > 0 {
					b.WriteString(" ")
				} else {
					b.WriteString("\n")
				}
			case "tc":
				if inCell > 0 {
					inCell--
				}
				b.WriteString("\t")
			case "tr":
				b.WriteString("\n")
			}
		}
	}
	return collapse(b.String())
}

var (
	tagRe   = regexp.MustCompile(`(?s)<script.*?</script>|<style.*?</style>|<[^>]+>`)
	multiNL = regexp.MustCompile(`\n{3,}`)
	rtfCtl  = regexp.MustCompile(`\\[a-z]+-?\d* ?|[{}]`)
)

func stripTags(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`).Replace(s)
	return collapse(s)
}

func rtf(s string) string {
	return collapse(rtfCtl.ReplaceAllString(s, ""))
}

func collapse(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(strings.ReplaceAll(l, " \t", "\t"), " ")
	}
	return strings.TrimSpace(multiNL.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// plain декодирует текст: UTF-8 с BOM или без, UTF-16, иначе cp1251.
func plain(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return string(data[3:])
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		return utf16le(data[2:])
	case utf8.Valid(data):
		return string(data)
	}
	out, err := charmap.Windows1251.NewDecoder().Bytes(data)
	if err != nil {
		return string(data)
	}
	return string(out)
}

func utf16le(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	var sb strings.Builder
	for i := 0; i < len(u); i++ {
		r := rune(u[i])
		if r >= 0xD800 && r < 0xDC00 && i+1 < len(u) {
			r = (r-0xD800)<<10 + (rune(u[i+1]) - 0xDC00) + 0x10000
			i++
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

func looksText(data []byte) bool {
	n := len(data)
	if n > 4096 {
		n = 4096
	}
	bin := 0
	for _, c := range data[:n] {
		if c == 0 || (c < 32 && c != '\n' && c != '\r' && c != '\t') {
			bin++
		}
	}
	return bin*100 < n
}

// Cut обрезает текст до limit рун с пометкой.
func Cut(s string, limit int) (string, bool) {
	r := []rune(s)
	if len(r) <= limit {
		return s, false
	}
	return string(r[:limit]), true
}
