package textx

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"regexp"
	"strconv"

	_ "image/gif" // декодеры для документов-картинок
	_ "image/png"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/ccitt"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// Image картинка для модели: скан страницы или документ-изображение.
type Image struct {
	MIME string
	Data []byte
	W, H int
}

// MaxImageSide длинная сторона картинки после уменьшения. Больше модели не нужно:
// Claude сам ужимает изображения примерно до 1568 точек, а лишние мегабайты
// идут по сети и в контекст.
const MaxImageSide = 1600

// ImageExt сообщает, что расширение означает документ-картинку.
func ImageExt(ext string) bool {
	switch ext {
	case "jpg", "jpeg", "png", "gif", "bmp", "tif", "tiff", "webp":
		return true
	}
	return false
}

// DocumentImage приводит документ-картинку к JPEG разумного размера.
// У многостраничного TIFF берётся первая страница.
func DocumentImage(data []byte) (Image, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Image{}, fmt.Errorf("картинка не прочитана: %w", err)
	}
	return toJPEG(img)
}

// PDFImages достаёт картинки страниц скана: по одной самой крупной на страницу,
// в порядке файла, начиная с from (с нуля), не больше max. total сколько всего
// картинок, пригодных как страницы; skipped сколько нашлось в форматах, которые
// не разобрать (JBIG2, JPEG 2000, необычные цветовые пространства).
//
// Разбор идёт прямо по байтам файла, а не через pdf.Reader: библиотека не отдаёт
// поток без раскодирования и паникует на DCTDecode и CCITTFaxDecode. Потоки объектов
// (PDF 1.5) этому не мешают: в них лежат только словари, а картинки всегда
// отдельные объекты верхнего уровня.
func PDFImages(data []byte, from, max int) (imgs []Image, total, skipped int, err error) {
	if bytes.Contains(data, []byte("/Encrypt")) {
		return nil, 0, 0, errors.New("pdf зашифрован, картинки страниц не достать")
	}
	objs := scanImageObjects(data)
	masks := map[string]bool{}
	for _, o := range objs {
		if m := reSMask.FindSubmatch(o.dict); m != nil {
			masks[string(m[1])] = true
		}
	}
	for _, o := range objs {
		if masks[o.id] || reImageMask.Match(o.dict) {
			continue
		}
		w, h := dictInt(o.dict, "Width"), dictInt(o.dict, "Height")
		// Логотипы, штампы и подписи на текстовых страницах страницей скана не считаем.
		if w < 300 || h < 300 {
			continue
		}
		if total < from || len(imgs) >= max {
			total++
			continue
		}
		img, e := decodePDFImage(o, w, h)
		if e != nil {
			skipped++
			continue
		}
		out, e := toJPEG(img)
		if e != nil {
			skipped++
			continue
		}
		imgs = append(imgs, out)
		total++
	}
	return imgs, total, skipped, nil
}

type pdfObj struct {
	id   string // «12 0»
	dict []byte
	body []byte // поток как есть, до раскодирования
}

var (
	reObj       = regexp.MustCompile(`(\d+)\s+(\d+)\s+obj\s*<<`)
	reSubImage  = regexp.MustCompile(`/Subtype\s*/Image\b`)
	reSMask     = regexp.MustCompile(`/SMask\s+(\d+\s+\d+)\s+R`)
	reImageMask = regexp.MustCompile(`/ImageMask\s+true`)
	reLength    = regexp.MustCompile(`/Length\s+(\d+)(\s+\d+\s+R)?`)
	reFilter    = regexp.MustCompile(`/Filter\s*(?:\[\s*)?/(\w+)`)
	reFilters   = regexp.MustCompile(`/Filter\s*\[([^\]]*)\]`)
	reName      = regexp.MustCompile(`/(\w+)`)
)

// scanImageObjects находит объекты-картинки с потоками.
func scanImageObjects(data []byte) []pdfObj {
	var out []pdfObj
	for _, loc := range reObj.FindAllSubmatchIndex(data, -1) {
		dictStart := loc[1] - 2
		dictEnd := matchDict(data, dictStart)
		if dictEnd < 0 {
			continue
		}
		dict := data[dictStart:dictEnd]
		if !reSubImage.Match(dict) {
			continue
		}
		rest := data[dictEnd:]
		i := 0
		for i < len(rest) && isSpace(rest[i]) {
			i++
		}
		if !bytes.HasPrefix(rest[i:], []byte("stream")) {
			continue
		}
		i += len("stream")
		if i < len(rest) && rest[i] == '\r' {
			i++
		}
		if i < len(rest) && rest[i] == '\n' {
			i++
		}
		start := dictEnd + i
		var body []byte
		if m := reLength.FindSubmatch(dict); m != nil && len(m[2]) == 0 {
			n, _ := strconv.Atoi(string(m[1]))
			if start+n <= len(data) {
				body = data[start : start+n]
			}
		}
		if body == nil {
			// Длина ссылкой на другой объект: ищем конец потока по метке.
			end := bytes.Index(data[start:], []byte("endstream"))
			if end < 0 {
				continue
			}
			body = bytes.TrimRight(data[start:start+end], "\r\n")
		}
		out = append(out, pdfObj{id: string(data[loc[2]:loc[3]]) + " " + string(data[loc[4]:loc[5]]), dict: dict, body: body})
	}
	return out
}

// matchDict возвращает позицию сразу за словарём, начинающимся с «<<» в start.
func matchDict(data []byte, start int) int {
	depth := 0
	for i := start; i < len(data)-1; i++ {
		switch {
		case data[i] == '(':
			// Строка в словаре: пропускаем с учётом экранирования и вложенных скобок.
			lvl := 0
			for ; i < len(data); i++ {
				if data[i] == '\\' {
					i++
					continue
				}
				if data[i] == '(' {
					lvl++
				} else if data[i] == ')' {
					lvl--
					if lvl == 0 {
						break
					}
				}
			}
		case data[i] == '<' && data[i+1] == '<':
			depth++
			i++
		case data[i] == '>' && data[i+1] == '>':
			depth--
			i++
			if depth == 0 {
				return i + 1
			}
		}
		if i-start > 64<<10 {
			return -1 // словарь картинки таким не бывает
		}
	}
	return -1
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\r' || b == '\n' || b == '\t' || b == '\f' || b == 0
}

func dictInt(dict []byte, key string) int {
	m := regexp.MustCompile(`/` + key + `\s+(-?\d+)`).FindSubmatch(dict)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(string(m[1]))
	return n
}

func dictBool(dict []byte, key string) bool {
	return regexp.MustCompile(`/` + key + `\s+true`).Match(dict)
}

func filters(dict []byte) []string {
	if m := reFilters.FindSubmatch(dict); m != nil {
		var out []string
		for _, n := range reName.FindAllSubmatch(m[1], -1) {
			out = append(out, string(n[1]))
		}
		return out
	}
	if m := reFilter.FindSubmatch(dict); m != nil {
		return []string{string(m[1])}
	}
	return nil
}

func decodePDFImage(o pdfObj, w, h int) (image.Image, error) {
	fs := filters(o.dict)
	body := o.body
	// Flate поверх другого сжатия встречается у некоторых генераторов: снимаем его.
	for len(fs) > 1 && fs[0] == "FlateDecode" {
		b, err := inflate(body)
		if err != nil {
			return nil, err
		}
		body, fs = b, fs[1:]
	}
	f := ""
	if len(fs) == 1 {
		f = fs[0]
	} else if len(fs) > 1 {
		return nil, fmt.Errorf("цепочка фильтров %v", fs)
	}
	switch f {
	case "DCTDecode":
		return jpeg.Decode(bytes.NewReader(body))
	case "CCITTFaxDecode":
		return decodeCCITT(o.dict, body, w, h)
	case "FlateDecode", "":
		raw := body
		if f == "FlateDecode" {
			b, err := inflate(body)
			if err != nil {
				return nil, err
			}
			raw = b
		}
		return rawImage(o.dict, raw, w, h)
	}
	return nil, fmt.Errorf("фильтр %s не поддерживается", f)
}

func inflate(b []byte) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(io.LimitReader(zr, 256<<20))
}

func decodeCCITT(dict, body []byte, w, h int) (image.Image, error) {
	k := dictInt(dict, "K")
	cols := dictInt(dict, "Columns")
	if cols == 0 {
		cols = 1728
	}
	if w > 0 {
		cols = w
	}
	var sf ccitt.SubFormat
	switch {
	case k < 0:
		sf = ccitt.Group4
	case k == 0:
		sf = ccitt.Group3
	default:
		return nil, errors.New("CCITT Group 3 2D не поддерживается")
	}
	rows := dictInt(dict, "Rows")
	if rows == 0 {
		rows = h
	}
	if rows == 0 {
		rows = ccitt.AutoDetectHeight
	}
	opts := &ccitt.Options{Align: dictBool(dict, "EncodedByteAlign"), Invert: dictBool(dict, "BlackIs1")}
	bits, err := io.ReadAll(ccitt.NewReader(bytes.NewReader(body), ccitt.MSB, sf, cols, rows, opts))
	if err != nil && len(bits) == 0 {
		return nil, err
	}
	stride := (cols + 7) / 8
	n := len(bits) / stride
	img := image.NewGray(image.Rect(0, 0, cols, n))
	for y := 0; y < n; y++ {
		row := bits[y*stride : (y+1)*stride]
		for x := 0; x < cols; x++ {
			if row[x/8]&(0x80>>uint(x%8)) != 0 {
				img.Pix[y*img.Stride+x] = 0xFF
			}
		}
	}
	return img, nil
}

// rawImage несжатые отсчёты: DeviceGray 1 и 8 бит, DeviceRGB 8 бит.
// PNG-предикторы (Predictor ≥ 10) снимаются по строкам.
func rawImage(dict, raw []byte, w, h int) (image.Image, error) {
	bpc := dictInt(dict, "BitsPerComponent")
	comps := 0
	switch {
	case bytes.Contains(dict, []byte("/DeviceGray")):
		comps = 1
	case bytes.Contains(dict, []byte("/DeviceRGB")):
		comps = 3
	default:
		return nil, errors.New("цветовое пространство не поддерживается")
	}
	if bpc != 8 && !(bpc == 1 && comps == 1) {
		return nil, fmt.Errorf("%d бит на компонент не поддерживается", bpc)
	}
	stride := (w*comps*bpc + 7) / 8
	if dictInt(dict, "Predictor") >= 10 {
		var err error
		if raw, err = unpredict(raw, stride, (comps*bpc+7)/8, h); err != nil {
			return nil, err
		}
	}
	if len(raw) < stride*h {
		return nil, errors.New("поток картинки короче заявленного размера")
	}
	if comps == 3 {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				p := raw[y*stride+x*3:]
				img.SetRGBA(x, y, color.RGBA{p[0], p[1], p[2], 0xFF})
			}
		}
		return img, nil
	}
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		row := raw[y*stride:]
		for x := 0; x < w; x++ {
			if bpc == 8 {
				img.Pix[y*img.Stride+x] = row[x]
			} else if row[x/8]&(0x80>>uint(x%8)) != 0 {
				img.Pix[y*img.Stride+x] = 0xFF
			}
		}
	}
	return img, nil
}

// unpredict снимает PNG-фильтры строк (None, Sub, Up, Average, Paeth).
func unpredict(src []byte, stride, bpp, h int) ([]byte, error) {
	out := make([]byte, 0, stride*h)
	prev := make([]byte, stride)
	for y := 0; y < h; y++ {
		off := y * (stride + 1)
		if off+stride+1 > len(src) {
			return nil, errors.New("поток картинки обрывается")
		}
		ft, cur := src[off], append([]byte(nil), src[off+1:off+1+stride]...)
		for i := range cur {
			var a, b, c byte
			if i >= bpp {
				a, c = cur[i-bpp], prev[i-bpp]
			}
			b = prev[i]
			switch ft {
			case 1:
				cur[i] += a
			case 2:
				cur[i] += b
			case 3:
				cur[i] += byte((int(a) + int(b)) / 2)
			case 4:
				cur[i] += paeth(a, b, c)
			}
		}
		out = append(out, cur...)
		prev = cur
	}
	return out, nil
}

func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := abs(p-int(a)), abs(p-int(b)), abs(p-int(c))
	if pa <= pb && pa <= pc {
		return a
	}
	if pb <= pc {
		return b
	}
	return c
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// toJPEG уменьшает картинку до MaxImageSide по длинной стороне и кодирует в JPEG.
// Перекодируем всегда: так CMYK-сканы становятся RGB, а размер предсказуемым.
func toJPEG(img image.Image) (Image, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return Image{}, errors.New("пустая картинка")
	}
	if w > MaxImageSide || h > MaxImageSide {
		scale := float64(MaxImageSide) / float64(max(w, h))
		nw, nh := max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))
		var dst draw.Image
		if _, gray := img.(*image.Gray); gray {
			dst = image.NewGray(image.Rect(0, 0, nw, nh))
		} else {
			dst = image.NewRGBA(image.Rect(0, 0, nw, nh))
		}
		// Сглаживание при уменьшении: у чёрно-белого скана тонкие штрихи букв
		// иначе пропадают.
		draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
		img, w, h = dst, nw, nh
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 82}); err != nil {
		return Image{}, err
	}
	return Image{MIME: "image/jpeg", Data: buf.Bytes(), W: w, H: h}, nil
}
