package rx

import (
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
