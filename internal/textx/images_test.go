package textx

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/drxinfra/rxmcp/internal/thirdparty/pdf"
)

// makePDF собирает минимальный корректный PDF: объекты по порядку, таблица ссылок,
// трейлер. objs[i] это тело объекта i+1 без «N 0 obj … endobj».
func makePDF(objs ...string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offs := make([]int, len(objs))
	for i, o := range objs {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, x)
	return b.Bytes()
}

func imageObj(dict string, body []byte) string {
	return fmt.Sprintf("<< /Type /XObject /Subtype /Image %s /Length %d >>\nstream\n%s\nendstream", dict, len(body), body)
}

// page страница с квадратом в левом верхнем углу: тёмный (0) на светлом (255).
func page(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 0xFF
	}
	for y := h / 10; y < h/4; y++ {
		for x := w / 10; x < w/4; x++ {
			img.SetGray(x, y, color.Gray{0})
		}
	}
	return img
}

func dark(t *testing.T, im Image, fx, fy float64) uint8 {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(im.Data))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	r, _, _, _ := img.At(int(fx*float64(b.Dx())), int(fy*float64(b.Dy()))).RGBA()
	return uint8(r >> 8)
}

func checkPage(t *testing.T, im Image) {
	t.Helper()
	if v := dark(t, im, 0.17, 0.17); v > 60 {
		t.Errorf("в квадрате светло (%d): картинка перевёрнута или испорчена", v)
	}
	if v := dark(t, im, 0.6, 0.6); v < 200 {
		t.Errorf("на поле темно (%d)", v)
	}
}

func TestPDFImagesDCT(t *testing.T) {
	var j bytes.Buffer
	jpeg.Encode(&j, page(2000, 3000), &jpeg.Options{Quality: 90})
	data := makePDF("<< /Type /Catalog >>",
		imageObj("/Width 2000 /Height 3000 /BitsPerComponent 8 /ColorSpace /DeviceGray /Filter /DCTDecode", j.Bytes()))
	imgs, total, skipped, err := PDFImages(data, 0, 5)
	if err != nil || len(imgs) != 1 || total != 1 || skipped != 0 {
		t.Fatalf("imgs=%d total=%d skipped=%d err=%v", len(imgs), total, skipped, err)
	}
	if imgs[0].H != MaxImageSide || imgs[0].W != 1066 {
		t.Errorf("размер после уменьшения %dx%d", imgs[0].W, imgs[0].H)
	}
	checkPage(t, imgs[0])
}

func TestPDFImagesFlatePredictorRGB(t *testing.T) {
	g := page(400, 500)
	// RGB со строковым фильтром Up (2) на каждой строке, как пишут PNG-предикторы.
	var raw bytes.Buffer
	prev := make([]byte, 400*3)
	for y := 0; y < 500; y++ {
		raw.WriteByte(2)
		for x := 0; x < 400; x++ {
			v := g.GrayAt(x, y).Y
			for c := 0; c < 3; c++ {
				raw.WriteByte(v - prev[x*3+c])
				prev[x*3+c] = v
			}
		}
	}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(raw.Bytes())
	zw.Close()
	data := makePDF("<< /Type /Catalog >>",
		imageObj("/Width 400 /Height 500 /BitsPerComponent 8 /ColorSpace /DeviceRGB /Filter /FlateDecode /DecodeParms << /Predictor 15 /Colors 3 /Columns 400 >>", z.Bytes()))
	imgs, _, skipped, err := PDFImages(data, 0, 5)
	if err != nil || len(imgs) != 1 {
		t.Fatalf("imgs=%d skipped=%d err=%v", len(imgs), skipped, err)
	}
	checkPage(t, imgs[0])
}

func TestPDFImagesOneBitSMaskLogoJBIG2(t *testing.T) {
	g := page(320, 400)
	stride := (320 + 7) / 8
	raw := make([]byte, stride*400)
	for y := 0; y < 400; y++ {
		for x := 0; x < 320; x++ {
			if g.GrayAt(x, y).Y > 128 {
				raw[y*stride+x/8] |= 0x80 >> uint(x%8)
			}
		}
	}
	data := makePDF("<< /Type /Catalog >>",
		// страница: однобитная без сжатия, с маской 3 0 R
		imageObj("/Width 320 /Height 400 /BitsPerComponent 1 /ColorSpace /DeviceGray /SMask 3 0 R", raw),
		// маска того же размера: страницей не считается
		imageObj("/Width 320 /Height 400 /BitsPerComponent 8 /ColorSpace /DeviceGray", bytes.Repeat([]byte{0xFF}, 320*400)),
		// логотип: мелкий, пропускается
		imageObj("/Width 120 /Height 60 /BitsPerComponent 8 /ColorSpace /DeviceGray", bytes.Repeat([]byte{0}, 120*60)),
		// JBIG2: не разбираем, но считаем
		imageObj("/Width 2480 /Height 3508 /BitsPerComponent 1 /ColorSpace /DeviceGray /Filter /JBIG2Decode", []byte{0, 0, 0, 1}))
	imgs, total, skipped, err := PDFImages(data, 0, 5)
	if err != nil || len(imgs) != 1 || total != 1 || skipped != 1 {
		t.Fatalf("imgs=%d total=%d skipped=%d err=%v", len(imgs), total, skipped, err)
	}
	checkPage(t, imgs[0])
}

func TestPDFImagesCCITTG4(t *testing.T) {
	// testdata/scan-g4.pdf собран libtiff (tiffdither -c g4, tiff2pdf) из картинки 1240×1754
	// с чёрным квадратом 100..300 и полосами-строками; BlackIs1 true.
	data, err := os.ReadFile("testdata/scan-g4.pdf")
	if err != nil {
		t.Fatal(err)
	}
	imgs, total, skipped, err := PDFImages(data, 0, 5)
	if err != nil || len(imgs) != 1 || total != 1 || skipped != 0 {
		t.Fatalf("imgs=%d total=%d skipped=%d err=%v", len(imgs), total, skipped, err)
	}
	if v := dark(t, imgs[0], 0.16, 0.11); v > 60 {
		t.Errorf("в квадрате светло (%d)", v)
	}
	if v := dark(t, imgs[0], 0.5, 0.1); v < 200 {
		t.Errorf("на поле темно (%d)", v)
	}
	if _, _, err := PDFText(data); !errors.Is(err, ErrScanned) {
		t.Errorf("скан без текста: ждали ErrScanned, получили %v", err)
	}
}

func TestPDFImagesPagingAndEncrypted(t *testing.T) {
	var j bytes.Buffer
	jpeg.Encode(&j, page(600, 800), nil)
	obj := imageObj("/Width 600 /Height 800 /BitsPerComponent 8 /ColorSpace /DeviceGray /Filter /DCTDecode", j.Bytes())
	data := makePDF("<< /Type /Catalog >>", obj, obj, obj)
	imgs, total, _, err := PDFImages(data, 1, 1)
	if err != nil || len(imgs) != 1 || total != 3 {
		t.Fatalf("страница 2 из 3: imgs=%d total=%d err=%v", len(imgs), total, err)
	}
	enc := append(append([]byte{}, data...), []byte("trailer << /Encrypt 9 0 R >>")...)
	if _, _, _, err := PDFImages(enc, 0, 5); err == nil {
		t.Error("зашифрованный pdf должен давать ошибку")
	}
}

func TestPDFTextSimple(t *testing.T) {
	content := "BT /F1 12 Tf 1 0 0 1 72 720 Tm (Hello world) Tj ET\nBT /F1 12 Tf 1 0 0 1 72 700 Tm [(Second)-400(line)] TJ ET"
	data := makePDF(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
	)
	// Одна строка текста на страницу это мало для «текстового» PDF, но и не скан:
	// проверяем раскладку, порог скана проверен на тесте G4.
	txt, info, err := PDFText(data)
	if err != nil && !errors.Is(err, ErrScanned) {
		t.Fatal(err)
	}
	if info.Pages != 1 {
		t.Errorf("страниц %d", info.Pages)
	}
	_ = txt
	page := pageTextForTest(t, data)
	if !strings.Contains(page, "Hello world\n") || !strings.Contains(page, "Second line") {
		t.Errorf("текст страницы: %q", page)
	}
}

func TestDocumentImage(t *testing.T) {
	var p bytes.Buffer
	png.Encode(&p, page(800, 1000))
	im, err := DocumentImage(p.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	checkPage(t, im)
	if _, err := DocumentImage([]byte("not an image")); err == nil {
		t.Error("мусор должен давать ошибку")
	}
}

func pageTextForTest(t *testing.T, data []byte) string {
	t.Helper()
	rd, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return pageText(rd.Page(1))
}
