package textx

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, c := range files {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(c))
	}
	zw.Close()
	return buf.Bytes()
}

func TestDocx(t *testing.T) {
	doc := `<?xml version="1.0"?><w:document xmlns:w="x"><w:body>
<w:p><w:r><w:t>Договор </w:t></w:r><w:r><w:t>№ 12</w:t></w:r></w:p>
<w:tbl><w:tr><w:tc><w:p><w:r><w:t>Сумма</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>100</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
<w:p><w:r><w:t>Конец</w:t></w:r></w:p></w:body></w:document>`
	data := makeZip(t, map[string]string{"word/document.xml": doc, "[Content_Types].xml": "<x/>"})
	got, err := Extract(data, "docx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Договор № 12") || !strings.Contains(got, "Сумма\t100\t") || !strings.HasSuffix(got, "Конец") {
		t.Errorf("docx text: %q", got)
	}
	// без расширения формат угадывается по zip
	if got2, err := Extract(data, ""); err != nil || got2 != got {
		t.Errorf("sniff: %v %q", err, got2)
	}
}

func TestXlsx(t *testing.T) {
	ss := `<sst><si><t>Товар</t></si><si><t>Цена</t></si></sst>`
	sheet := `<worksheet><sheetData><row><c t="s"><v>0</v></c><c t="s"><v>1</v></c></row><row><c><v>42</v></c><c><v>3.5</v></c></row></sheetData></worksheet>`
	data := makeZip(t, map[string]string{"xl/sharedStrings.xml": ss, "xl/worksheets/sheet1.xml": sheet})
	got, err := Extract(data, "xlsx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Товар\tЦена\t\n42\t3.5\t") {
		t.Errorf("xlsx text: %q", got)
	}
}

func TestPlainAndPDF(t *testing.T) {
	if got, _ := Extract([]byte("\xEF\xBB\xBFпривет"), "txt"); got != "привет" {
		t.Errorf("bom: %q", got)
	}
	cp := []byte{0xcf, 0xf0, 0xe8, 0xe2, 0xe5, 0xf2} // «Привет» в cp1251
	if got, _ := Extract(cp, "txt"); got != "Привет" {
		t.Errorf("cp1251: %q", got)
	}
	if _, err := Extract([]byte("%PDF-1.4 ..."), ""); err == nil || !strings.Contains(err.Error(), "pdf") {
		t.Errorf("pdf должен вернуть понятную ошибку, получили %v", err)
	}
	if got, _ := Extract([]byte("<p>Раз</p><script>x()</script><p>Два&nbsp;три</p>"), "html"); !strings.Contains(got, "Раз") || strings.Contains(got, "x()") || !strings.Contains(got, "Два три") {
		t.Errorf("html: %q", got)
	}
	if s, cut := Cut("абвгд", 3); s != "абв" || !cut {
		t.Errorf("cut: %q %v", s, cut)
	}
}
