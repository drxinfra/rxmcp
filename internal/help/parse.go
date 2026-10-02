// Package help — встроенная справка Directum RX: скачивание со стенда пользователя,
// локальный индекс и поиск. Справка принадлежит вендору, поэтому в поставке её нет:
// каждый строит индекс со своего стенда, и ответы соответствуют его версии системы.
package help

import (
	"html"
	"regexp"
	"strings"
)

// Topic одна статья справки.
type Topic struct {
	File   string   // имя файла, оно же идентификатор: sungero_tasks.htm
	Title  string   // заголовок из оглавления
	Crumbs []string // путь по оглавлению, без самой статьи
	Text   string   // текст статьи: абзацы, таблицы строками, ссылки вида [текст](файл.htm)
}

var (
	reTOC   = regexp.MustCompile(`(?s)<li class="heading\d+" id="i([\d.]+)"[^>]*>\s*<a [^>]*href="([^"#]+)[^"]*"[^>]*>\s*<span[^>]*>(.*?)</span>`)
	reTitle = regexp.MustCompile(`(?is)<title>(.*?)</title>`)
	reTag   = regexp.MustCompile(`(?s)<[^>]+>`)
	reSpace = regexp.MustCompile(`[ \t\x{00a0}]+`)
	reBlank = regexp.MustCompile(`\n{3,}`)
)

// ParseTOC разбирает оглавление (hmcontent.htm): название справки и статьи в порядке оглавления.
// Вложенность берётся из номера пункта (i1.2.3), а не из вложенности тегов: так устойчивее к вёрстке.
func ParseTOC(page []byte) (product string, topics []Topic) {
	s := string(page)
	if m := reTitle.FindStringSubmatch(s); m != nil {
		product = clean(m[1])
	}
	titles := map[string]string{}
	seen := map[string]bool{}
	for _, m := range reTOC.FindAllStringSubmatch(s, -1) {
		id, file, title := m[1], m[2], clean(m[3])
		titles[id] = title
		if !strings.HasSuffix(file, ".htm") || seen[file] || strings.Contains(file, "/") {
			continue
		}
		seen[file] = true
		var crumbs []string
		parts := strings.Split(id, ".")
		for i := 1; i < len(parts); i++ {
			if t := titles[strings.Join(parts[:i], ".")]; t != "" {
				crumbs = append(crumbs, t)
			}
		}
		topics = append(topics, Topic{File: file, Title: title, Crumbs: crumbs})
	}
	return product, topics
}

func clean(s string) string {
	s = html.UnescapeString(reTag.ReplaceAllString(s, ""))
	return strings.TrimSpace(reSpace.ReplaceAllString(strings.ReplaceAll(s, "\n", " "), " "))
}

// ParseTopic вытаскивает из страницы статьи читаемый текст. Тело лежит между служебными
// метками поисковика ZOOMRESTART и ZOOMSTOP; если их нет, берём всё после id="idcontent".
func ParseTopic(page []byte) string {
	s := string(page)
	if i := strings.Index(s, "<!--ZOOMRESTART-->"); i >= 0 {
		s = s[i+len("<!--ZOOMRESTART-->"):]
		if j := strings.Index(s, "<!--ZOOMSTOP-->"); j >= 0 {
			s = s[:j]
		}
	} else if i := strings.Index(s, `id="idcontent"`); i >= 0 {
		s = s[i:]
	}
	return toText(s)
}

// toText превращает HTML в текст: блочные теги дают перевод строки, ячейки таблицы разделяются « | »,
// внутренние ссылки остаются как [текст](файл.htm), чтобы помощник мог перейти к связанной статье.
func toText(s string) string {
	var b strings.Builder
	b.Grow(len(s) / 3)
	skip := "" // имя тега, содержимое которого пропускаем (script, style)
	href := "" // адрес открытой внутренней ссылки
	linkStart := 0
	for len(s) > 0 {
		i := strings.IndexByte(s, '<')
		if i < 0 {
			if skip == "" {
				b.WriteString(html.UnescapeString(s))
			}
			break
		}
		if i > 0 && skip == "" {
			b.WriteString(html.UnescapeString(s[:i]))
		}
		s = s[i:]
		j := strings.IndexByte(s, '>')
		if j < 0 {
			break
		}
		tag := s[1:j]
		s = s[j+1:]
		if strings.HasPrefix(tag, "!--") {
			continue
		}
		closing := strings.HasPrefix(tag, "/")
		name := strings.ToLower(strings.TrimLeft(tag, "/"))
		if k := strings.IndexAny(name, " \t\n/"); k >= 0 {
			name = name[:k]
		}
		if skip != "" {
			if closing && name == skip {
				skip = ""
			}
			continue
		}
		switch name {
		case "script", "style":
			if !closing {
				skip = name
			}
		case "br":
			b.WriteByte('\n')
		case "p", "div", "tr", "table", "ul", "ol", "h1", "h2", "h3", "h4", "h5", "h6":
			b.WriteByte('\n')
		case "li":
			if !closing {
				b.WriteString("\n- ")
			}
		case "td", "th":
			if closing {
				b.WriteString(" | ")
			}
		case "a":
			if closing {
				if href != "" {
					txt := strings.TrimSpace(b.String()[linkStart:])
					if txt != "" {
						cut := b.String()[:linkStart]
						b.Reset()
						b.WriteString(cut)
						b.WriteString("[" + txt + "](" + href + ")")
					}
					href = ""
				}
				continue
			}
			href = ""
			if m := reHref.FindStringSubmatch(tag); m != nil {
				h := m[1]
				if k := strings.IndexByte(h, '#'); k >= 0 {
					h = h[:k]
				}
				if strings.HasSuffix(h, ".htm") && !strings.Contains(h, "/") {
					href, linkStart = h, b.Len()
				}
			}
		}
	}
	out := b.String()
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		l = strings.TrimSpace(reSpace.ReplaceAllString(l, " "))
		lines[i] = strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(l, "|")), "|")
	}
	return strings.TrimSpace(reBlank.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

var reHref = regexp.MustCompile(`href="([^"]+)"`)
