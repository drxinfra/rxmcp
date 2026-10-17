package textx

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/drxinfra/rxmcp/internal/thirdparty/pdf"
)

// ErrScanned PDF без текстового слоя: страницы это картинки (скан).
// Текст из него берётся не здесь, а картинками страниц через PDFImages.
var ErrScanned = errors.New("pdf без текстового слоя (скан)")

// PDFInfo что удалось узнать о PDF при извлечении текста.
type PDFInfo struct {
	Pages     int
	TextPages int // страниц, где нашёлся текст
	Letters   int // букв во всём тексте
}

// Sparse мало текста для такого числа страниц: скорее всего скан, на котором
// распознаны или впечатаны отдельные надписи (номер, дата, штамп).
func (i PDFInfo) Sparse() bool { return i.Pages > 0 && i.Letters < 300*i.Pages }

// PDFText извлекает текстовый слой PDF. Строки собираются по координатам глифов,
// а не порядку в потоке: генераторы PDF пишут текст кусками в любом порядке,
// и простое склеивание даёт слова без пробелов и перемешанные строки.
// Страницы разделяются строкой «— стр. N —». Если текста почти нет, возвращает
// ErrScanned вместе с числом страниц.
func PDFText(data []byte) (text string, info PDFInfo, err error) {
	defer func() {
		// Библиотека паникует на битых и необычных файлах (неизвестный фильтр,
		// повреждённая таблица ссылок): для пользователя это просто ошибка чтения.
		if r := recover(); r != nil {
			err = fmt.Errorf("pdf не разобран: %v", r)
		}
	}()
	rd, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		if strings.Contains(err.Error(), "encrypted") || strings.Contains(err.Error(), "password") {
			return "", info, errors.New("pdf защищён паролем")
		}
		return "", info, fmt.Errorf("pdf не разобран: %w", err)
	}
	info.Pages = rd.NumPage()
	var b strings.Builder
	letters := 0
	for i := 1; i <= info.Pages; i++ {
		page := pageText(rd.Page(i))
		n := countLetters(page)
		letters += n
		if n > 0 {
			info.TextPages++
		}
		if info.Pages > 1 {
			fmt.Fprintf(&b, "\n— стр. %d —\n", i)
		}
		b.WriteString(page)
	}
	info.Letters = letters
	// Скан с редкими служебными надписями (штамп, номер страницы) тоже скан.
	if letters < 40*info.Pages {
		return "", info, ErrScanned
	}
	return strings.TrimSpace(b.String()), info, nil
}

// pageText текст одной страницы. Строки берутся по позициям текстовых блоков
// (GetTextByRow): поглифовые координаты библиотеки для PDF из Word неверны и дают
// перевёрнутые слова, а позиции блоков надёжны. Ошибка в одной странице не роняет
// весь документ: страница просто остаётся пустой.
func pageText(p pdf.Page) (out string) {
	defer func() {
		if r := recover(); r != nil {
			out = ""
		}
	}()
	if p.V.IsNull() {
		return ""
	}
	rows, err := p.GetTextByRow()
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, row := range rows {
		sort.SliceStable(row.Content, func(i, j int) bool { return row.Content[i].X < row.Content[j].X })
		var sb strings.Builder
		var end float64 // где примерно кончился предыдущий кусок
		for i, w := range row.Content {
			// Колонки рядом (макеты, таблицы без линий) склеиваются в одну строку.
			// Ширину куска библиотека не знает, оцениваем по кеглю: полкегля на букву.
			// Разрыв больше трёх кеглей ставим табуляцией.
			if i > 0 && w.FontSize > 0 && w.X-end > 3*w.FontSize {
				sb.WriteByte('\t')
			}
			sb.WriteString(w.S)
			// Куски без своего сдвига стоят в одной точке, но на деле идут друг за другом.
			start := max(w.X, end)
			end = start + float64(len([]rune(w.S)))*w.FontSize*0.5
		}
		if s := strings.TrimSpace(collapseSpaces(sb.String())); s != "" {
			b.WriteString(s)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func collapseSpaces(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if r == ' ' || r == ' ' {
			if !space {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

func countLetters(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}
