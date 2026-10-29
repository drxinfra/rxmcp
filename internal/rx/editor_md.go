package rx

import (
	"regexp"
	"strconv"
	"strings"
)

// Описание карточки на доске хранится в markdown, а редактор веб-клиента при открытии
// разбирает его и собирает заново по своим правилам (prosemirror-markdown). Если
// собранный текст отличается от сохранённого, карточка считается изменённой и клиент
// показывает кнопку «Сохранить», а нажатие пишет в историю «Описание изменено».
// Поэтому описание пишем сразу в том виде, в каком его сохраняет редактор. Правила
// сняты с описаний, сохранённых редактором RX 25.2 (история карточек):
//   - маркер списка «* », пункты без пустых строк, вложенный маркированный список
//     сразу на следующей строке с отступом под текст пункта;
//   - вложенный нумерованный список отделяется пустой строкой и строкой из одного
//     отступа: «* Сделать:\n\n  \n  1. …»;
//   - номера выравниваются по ширине самого длинного, отступ вложенного под ним
//     равен ширине «N. »;
//   - экранируются \ ` * ~ [ ], кроме разметки (**жирный**, *курсив*, ~~зачёркнутый~~,
//     `код`, [ссылка](адрес)); `_` не экранируется;
//   - в начале абзаца и строки после переноса экранируются «12.» → «12\.» и «-»,
//     «+», «>»; в пункте списка нет;
//   - абзацы и блоки разделены пустой строкой; перед нумерованным списком лишняя
//     строка (две пустые после другого блока, перевод строки в начале текста);
//     текст кончается пустой строкой;
//     если последний блок список, редактор добавляет после него пустой абзац,
//     который сохраняется как «\»: хвост «\n\n\\\n\n\n».
// Простой перенос строки внутри абзаца редактор превращает в пробел, поэтому каждая
// строка обычного текста становится отдельным абзацем: так задумано в исходном тексте.

const editorListTail = "\n\n\\\n\n\n"

var (
	mdItem    = regexp.MustCompile(`^( *)([-*+]|\d+[.)])[ \t]+(.*)$`)
	mdHeading = regexp.MustCompile(`^(#{1,6})[ \t]+(.*)$`)
	mdNumPara = regexp.MustCompile(`^(\d+)\.`)
	mdNumItem = regexp.MustCompile(`^(\d+)[.)][ \t]+(.*)$`)
	mdLink    = regexp.MustCompile(`^\[([^\[\]\n]+)\]\(([^()\s]+)\)`)
)

type mdBlock struct {
	kind    string // p, h, code, list
	text    string
	level   int
	ordered bool
	start   int
	items   []mdItemNode
}

type mdItemNode struct {
	text string
	kids []mdBlock
	// emptyHead: первая строка пункта пустая («* \\»), под ней вложенный список
	emptyHead bool
}

// EditorMarkdown приводит описание карточки к виду, в котором его сохраняет редактор
// веб-клиента. Текст, уже прошедший через редактор, не меняется.
func EditorMarkdown(src string) string {
	if strings.TrimSpace(src) == "" {
		return ""
	}
	blocks := mdParse(src)
	if len(blocks) == 0 {
		return ""
	}
	var out string
	for k, b := range blocks {
		switch {
		case b.kind == "list" && b.ordered && k == 0:
			// перед нумерованным списком редактор всегда оставляет лишнюю строку,
			// даже в начале текста
			out += "\n"
		case k == 0:
		case b.kind == "list" && b.ordered:
			out += "\n\n\n"
		default:
			out += "\n\n"
		}
		out += mdRender(b)
	}
	if blocks[len(blocks)-1].kind == "list" {
		return out + editorListTail
	}
	return out + "\n\n"
}

func mdLead(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }

func mdFence(l string) bool {
	t := strings.TrimLeft(l, " ")
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}

// mdItemCol столбец, с которого начинается текст пункта.
func mdItemCol(m []string) int { return len(m[1]) + len(m[2]) + 1 }

// mdBreaksPara строка начинает новый блок вместо продолжения абзаца после переноса:
// маркированный пункт, нумерованный с единицы (другие номера абзац не прерывают),
// ограждение кода.
func mdBreaksPara(l string) bool {
	if mdFence(l) {
		return true
	}
	m := mdItem.FindStringSubmatch(l)
	if m == nil {
		return false
	}
	return !(m[2][0] >= '0' && m[2][0] <= '9') || m[2][:len(m[2])-1] == "1"
}

func mdFenceClose(l string, c byte) bool {
	t := strings.TrimSpace(l)
	return len(t) >= 3 && strings.Trim(t, string(c)) == ""
}

func mdParse(src string) []mdBlock {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(src, "\n")
	var blocks []mdBlock
	for i := 0; i < len(lines); {
		l := lines[i]
		t := strings.TrimSpace(l)
		switch {
		case t == "":
			i++
		case mdFence(l):
			fence := strings.TrimLeft(l, " ")[:3]
			j := i + 1
			// закрывает только строка из одних знаков ограждения, «```bash» уже открытие
			for j < len(lines) && !mdFenceClose(lines[j], fence[0]) {
				j++
			}
			end := j + 1
			if end > len(lines) {
				end = len(lines)
			}
			// код переносится как есть, с табуляциями и пробелами в концах строк
			code := append([]string{strings.TrimLeft(lines[i], " ")}, lines[i+1:end]...)
			if j >= len(lines) {
				code = append(code, fence)
			}
			blocks = append(blocks, mdBlock{kind: "code", text: strings.Join(code, "\n")})
			i = end
		case mdItem.MatchString(l):
			var b mdBlock
			b, i = mdParseList(lines, i, mdLead(l))
			blocks = append(blocks, b)
		case mdHeading.MatchString(t):
			m := mdHeading.FindStringSubmatch(t)
			blocks = append(blocks, mdBlock{kind: "h", level: len(m[1]), text: strings.TrimSpace(m[2])})
			i++
		default:
			// Каждая строка обычного текста отдельный абзац. Строка, которая кончается
			// на «\», это перенос внутри абзаца (так его пишет редактор), абзац идёт
			// дальше; одиночная «\» пустой абзац. Пробелы в конце строки редактор
			// сохраняет. Части абзаца хранятся через \n.
			var segs []string
			cur := strings.TrimLeft(l, " ")
			i++
			for strings.HasSuffix(cur, `\`) && !strings.HasSuffix(cur, `\\`) {
				segs = append(segs, strings.TrimSuffix(cur, `\`))
				if i < len(lines) && strings.TrimSpace(lines[i]) != "" && !mdBreaksPara(lines[i]) {
					cur = strings.TrimLeft(lines[i], " ")
					i++
					continue
				}
				cur = ""
				break
			}
			segs = append(segs, cur)
			blocks = append(blocks, mdBlock{kind: "p", text: strings.Join(segs, "\n")})
		}
	}
	return blocks
}

func mdParseList(lines []string, i, ind int) (mdBlock, int) {
	m := mdItem.FindStringSubmatch(lines[i])
	ordered := m[2][0] >= '0' && m[2][0] <= '9'
	b := mdBlock{kind: "list", ordered: ordered, start: 1}
	if ordered {
		b.start, _ = strconv.Atoi(m[2][:len(m[2])-1])
	}
	// Пункты одного списка: одинаковый отступ маркера, у нумерованного ещё и
	// одинаковое начало текста (номера редактор выравнивает: « 9.», «10.»).
	col := mdItemCol(m)
	sibling := func(m []string) bool {
		if m == nil || (m[2][0] >= '0' && m[2][0] <= '9') != ordered {
			return false
		}
		return len(m[1]) == ind || ordered && mdItemCol(m) == col
	}
	for i < len(lines) {
		m := mdItem.FindStringSubmatch(lines[i])
		if !sibling(m) {
			break
		}
		ind = len(m[1])
		it := mdItemNode{text: strings.TrimSpace(m[3])}
		if it.text == `\` {
			it.text, it.emptyHead = "", true
		} else if n := mdNumItem.FindStringSubmatch(it.text); !ordered && n != nil {
			// «* 1. текст» редактор читает как пункт с вложенным нумерованным списком
			// и сохраняет пустой первой строкой пункта
			start, _ := strconv.Atoi(n[1])
			it.text, it.emptyHead = "", true
			it.kids = append(it.kids, mdBlock{kind: "list", ordered: true, start: start, items: []mdItemNode{{text: n[2]}}})
		}
		i++
		for i < len(lines) {
			if strings.TrimSpace(lines[i]) == "" {
				// вложенный список редактора идёт после пустой строки и строки из отступа
				j := i
				for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
					j++
				}
				if j < len(lines) && mdItem.MatchString(lines[j]) && mdLead(lines[j]) > ind {
					i = j
					continue
				}
				break
			}
			if strings.TrimSpace(lines[i]) == `\` {
				break
			}
			lead := mdLead(lines[i])
			if mi := mdItem.FindStringSubmatch(lines[i]); mi != nil {
				if lead <= ind || sibling(mi) {
					break
				}
				var kid mdBlock
				kid, i = mdParseList(lines, i, lead)
				it.kids = append(it.kids, kid)
				continue
			}
			// продолжение текста пункта с отступом; строка без отступа уже новый абзац
			if lead <= ind || len(it.kids) > 0 || mdFence(lines[i]) {
				break
			}
			it.text += " " + strings.TrimSpace(lines[i])
			i++
		}
		b.items = append(b.items, it)
	}
	return b, i
}

func mdRender(b mdBlock) string {
	switch b.kind {
	case "code":
		return b.text
	case "h":
		return strings.Repeat("#", b.level) + " " + mdInline(b.text)
	case "list":
		return mdRenderList(b, "")
	}
	segs := strings.Split(b.text, "\n")
	for k := range segs {
		// после переноса строки начало строки экранируется так же, как начало абзаца
		segs[k] = mdParaStart(mdInline(segs[k]))
	}
	return strings.Join(segs, "\\\n")
}

func mdRenderList(b mdBlock, indent string) string {
	w := len(strconv.Itoa(b.start + len(b.items) - 1))
	out := make([]string, 0, len(b.items))
	for k, it := range b.items {
		mark := "* "
		if b.ordered {
			n := strconv.Itoa(b.start + k)
			mark = strings.Repeat(" ", w-len(n)) + n + ". "
		}
		sub := indent + strings.Repeat(" ", len(mark))
		s := indent + mark + mdInline(it.text)
		if it.emptyHead {
			s = indent + mark + `\`
		}
		for k, kid := range it.kids {
			if it.emptyHead && k == 0 {
				s += "\n" + sub + "\n" + mdRenderList(kid, sub)
			} else if kid.ordered {
				s += "\n\n" + sub + "\n" + mdRenderList(kid, sub)
			} else {
				s += "\n" + mdRenderList(kid, sub)
			}
		}
		out = append(out, s)
	}
	return strings.Join(out, "\n")
}

// mdParaStart экранирует то, что в начале абзаца редактор прочёл бы как разметку.
func mdParaStart(s string) string {
	if m := mdNumPara.FindStringSubmatchIndex(s); m != nil {
		return s[:m[3]] + `\` + s[m[3]:]
	}
	if s != "" && strings.ContainsRune("-+>", rune(s[0])) {
		return `\` + s
	}
	return s
}

func mdIsPunct(c byte) bool {
	return c < 0x80 && strings.IndexByte("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", c) >= 0
}

func mdWordByte(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c >= 0x80 || c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func mdSpace(s string, i int) bool {
	return i < 0 || i >= len(s) || s[i] == ' ' || s[i] == '\n'
}

// mdPair ищет закрывающий разделитель delim после позиции from: перед ним не пробел.
func mdPair(s string, from int, delim string) int {
	for j := from; j+len(delim) <= len(s); j++ {
		if s[j] == '\\' {
			j++
			continue
		}
		if strings.HasPrefix(s[j:], delim) && j > from && !mdSpace(s, j-1) {
			return j
		}
	}
	return -1
}

// mdInline экранирует текст абзаца, сохраняя разметку: код, жирный, курсив,
// зачёркнутый и ссылки. Экранирование «\x», уже стоящее в тексте, сохраняется.
func mdInline(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\':
			if i+1 < len(s) && mdIsPunct(s[i+1]) {
				n := s[i+1]
				if strings.IndexByte("\\`*~[]", n) >= 0 {
					b.WriteByte('\\')
				}
				b.WriteByte(n)
				i += 2
				continue
			}
			b.WriteString(`\\`)
			i++
			continue
		case c == '`':
			run := 1
			for i+run < len(s) && s[i+run] == '`' {
				run++
			}
			ticks := s[i : i+run]
			if j := strings.Index(s[i+run:], ticks); j >= 0 {
				b.WriteString(s[i : i+run+j+run])
				i += run + j + run
				continue
			}
		case strings.HasPrefix(s[i:], "**") && !mdSpace(s, i+2):
			if j := mdPair(s, i+2, "**"); j > 0 {
				b.WriteString("**" + mdInline(s[i+2:j]) + "**")
				i = j + 2
				continue
			}
		case strings.HasPrefix(s[i:], "~~") && !mdSpace(s, i+2):
			if j := mdPair(s, i+2, "~~"); j > 0 {
				b.WriteString("~~" + mdInline(s[i+2:j]) + "~~")
				i = j + 2
				continue
			}
		case c == '[':
			if m := mdLink.FindStringSubmatch(s[i:]); m != nil {
				b.WriteString("[" + mdInline(m[1]) + "](" + m[2] + ")")
				i += len(m[0])
				continue
			}
		case c == '*' && !mdWordByte(s, i-1) && !mdSpace(s, i+1) && i+1 < len(s) && s[i+1] != '*':
			if j := mdPair(s, i+1, "*"); j > 0 && !mdWordByte(s, j+1) && (j+1 >= len(s) || s[j+1] != '*') {
				b.WriteString("*" + mdInline(s[i+1:j]) + "*")
				i = j + 1
				continue
			}
		}
		if strings.IndexByte("`*~[]", c) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}
