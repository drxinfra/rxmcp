package rx

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/drxinfra/rxmcp/internal/odata"
)

// Окончания, которые отрезаются от русских слов запроса, длинные раньше коротких:
// «согласование договоров» должно находить «Регламент согласования договоров».
var ruEndings = []string{
	"иями", "ями", "ами", "ого", "его", "ому", "ему", "ыми", "ими", "ией",
	"ой", "ей", "ий", "ый", "ая", "яя", "ое", "ее", "ые", "ие", "ов", "ев", "ам", "ям",
	"ах", "ях", "ом", "ем", "ию", "ия", "ии", "ья", "ью", "ым", "им",
	"а", "я", "ы", "и", "у", "ю", "е", "о", "ь",
}

// stem грубая основа слова для contains: отрезает падежное окончание, оставляя не меньше
// пяти букв (иначе «Петров» превратится в «Петр»). Латиница, числа и короткие слова не меняются.
func stem(w string) string {
	if utf8.RuneCountInString(w) < 6 {
		return w
	}
	for _, r := range w {
		if !unicode.Is(unicode.Cyrillic, r) {
			return w
		}
	}
	lw := strings.ToLower(w)
	for _, e := range ruEndings {
		if strings.HasSuffix(lw, e) && utf8.RuneCountInString(lw)-utf8.RuneCountInString(e) >= 5 {
			return string([]rune(w)[:utf8.RuneCountInString(w)-utf8.RuneCountInString(e)])
		}
	}
	return w
}

// wordsFilter условие OData «каждое слово запроса есть хотя бы в одном из полей»: слова
// через and, поля через or, у слов отрезаны окончания. Сравнение без учёта регистра через
// tolower: в PostgreSQL contains по названию документа регистр различает, «регламент» не
// находил «Регламент». Предлоги и союзы (до двух букв)
// пропускаются; если других слов нет, ищется вся строка.
func wordsFilter(q string, fields ...string) string {
	var words []string
	for _, w := range strings.FieldsFunc(q, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(",.;:!?«»\"()", r)
	}) {
		if utf8.RuneCountInString(w) > 2 {
			words = append(words, strings.ToLower(stem(w)))
		}
	}
	if len(words) == 0 {
		words = []string{strings.ToLower(strings.TrimSpace(q))}
	}
	var and []string
	for _, w := range words {
		var or []string
		for _, f := range fields {
			or = append(or, "contains(tolower("+f+"),"+odata.Quote(w)+")")
		}
		if len(or) == 1 {
			and = append(and, or[0])
		} else {
			and = append(and, "("+strings.Join(or, " or ")+")")
		}
	}
	return strings.Join(and, " and ")
}

// statusSynonyms приводит то, что пишут модели, к значениям status инструментов.
var statusSynonyms = map[string]string{
	"inprocess": "in_process", "in_progress": "in_process", "inprogress": "in_process", "active": "in_process",
	"open": "in_process", "pending": "in_process", "в_работе": "in_process",
	"done": "completed", "closed": "completed", "finished": "completed", "complete": "completed",
	"expired": "overdue", "late": "overdue", "просрочено": "overdue", "просроченные": "overdue",
	"new": "unread", "непрочитанные": "unread", "any": "all", "все": "all",
}

// normStatus разбирает status, который маленькие модели пишут вольно: «in_process, overdue»,
// «In Progress», «done». Несколько значений сводятся к самому широкому: просроченные и
// непрочитанные входят в работающие, иначе — все. Возвращает "" для пустого и исходную
// строку, если ничего не распознано (тогда вызывающий сообщит допустимые значения).
// Раньше «in_process, overdue» давало ошибку, и модель повторяла тот же вызов до лимита шагов.
func normStatus(s string, allowed ...string) string {
	var got []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return strings.ContainsRune(",;|/+", r) }) {
		w = strings.ReplaceAll(strings.TrimSpace(w), " ", "_")
		if v, ok := statusSynonyms[w]; ok {
			w = v
		}
		for _, a := range allowed {
			if w == a && !slices.Contains(got, w) {
				got = append(got, w)
			}
		}
	}
	switch {
	case len(got) == 0:
		return strings.TrimSpace(s)
	case len(got) == 1:
		return got[0]
	case slices.Contains(got, "all"):
		return "all"
	}
	inWork := true
	for _, g := range got {
		if g != "in_process" && g != "overdue" && g != "unread" {
			inWork = false
		}
	}
	if inWork && slices.Contains(allowed, "in_process") {
		return "in_process"
	}
	return "all"
}
