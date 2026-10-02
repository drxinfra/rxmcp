// Package catalog — каталог сущностей сервиса интеграции RX по его $metadata (OData CSDL).
// Нужен, чтобы помощник мог найти и прочитать любую сущность, а не только те, для которых есть готовый инструмент.
package catalog

import (
	"encoding/xml"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Field свойство или ссылка сущности.
type Field struct {
	Name string
	Type string
}

// Type тип сущности.
type Type struct {
	Name  string // с пространством имён
	Base  string
	Props []Field
	Navs  []Field
}

// Set набор сущностей: то, что стоит в адресе запроса.
type Set struct {
	Name string
	Type string
}

// Catalog разобранные метаданные.
type Catalog struct {
	Sets  []Set
	types map[string]*Type
}

type edmx struct {
	Schemas []struct {
		Namespace   string `xml:"Namespace,attr"`
		EntityTypes []struct {
			Name     string `xml:"Name,attr"`
			BaseType string `xml:"BaseType,attr"`
			Props    []struct {
				Name string `xml:"Name,attr"`
				Type string `xml:"Type,attr"`
			} `xml:"Property"`
			Navs []struct {
				Name string `xml:"Name,attr"`
				Type string `xml:"Type,attr"`
			} `xml:"NavigationProperty"`
		} `xml:"EntityType"`
		Containers []struct {
			Sets []struct {
				Name string `xml:"Name,attr"`
				Type string `xml:"EntityType,attr"`
			} `xml:"EntitySet"`
		} `xml:"EntityContainer"`
	} `xml:"DataServices>Schema"`
}

// Parse разбирает $metadata.
func Parse(data []byte) (*Catalog, error) {
	var e edmx
	if err := xml.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("$metadata не разобран: %w", err)
	}
	c := &Catalog{types: map[string]*Type{}}
	for _, s := range e.Schemas {
		for _, t := range s.EntityTypes {
			ty := &Type{Name: s.Namespace + "." + t.Name, Base: t.BaseType}
			for _, p := range t.Props {
				ty.Props = append(ty.Props, Field{p.Name, short(p.Type)})
			}
			for _, n := range t.Navs {
				ty.Navs = append(ty.Navs, Field{n.Name, short(n.Type)})
			}
			c.types[ty.Name] = ty
		}
		for _, k := range s.Containers {
			for _, es := range k.Sets {
				c.Sets = append(c.Sets, Set{es.Name, es.Type})
			}
		}
	}
	if len(c.Sets) == 0 {
		return nil, fmt.Errorf("в $metadata нет ни одного набора сущностей")
	}
	sort.Slice(c.Sets, func(i, j int) bool { return c.Sets[i].Name < c.Sets[j].Name })
	return c, nil
}

// short убирает пространство имён и обёртку коллекции: Collection(Sungero.Task) → Task[].
func short(t string) string {
	coll := strings.HasPrefix(t, "Collection(")
	t = strings.TrimSuffix(strings.TrimPrefix(t, "Collection("), ")")
	if i := strings.LastIndexByte(t, '.'); i >= 0 {
		t = t[i+1:]
	}
	if coll {
		t += "[]"
	}
	return t
}

// Русские слова, которыми пользователь называет сущности, и корни их английских имён в RX.
var ru = map[string][]string{
	"договор": {"contract"}, "контрагент": {"counterpart", "compan", "person", "bank"}, "организац": {"compan", "businessunit"},
	"сотрудник": {"employee"}, "подразделен": {"department"}, "должност": {"jobtitle"}, "задач": {"task"}, "задани": {"assignment"},
	"уведомлен": {"notice"}, "документ": {"document"}, "проект": {"project"}, "рол": {"role"}, "папк": {"folder"}, "приказ": {"order"},
	"письм": {"letter"}, "счет": {"invoice"}, "акт": {"act"}, "доверенност": {"powerofattorney"}, "персон": {"person"}, "банк": {"bank"},
	"валют": {"currenc"}, "город": {"cit"}, "стран": {"countr"}, "регион": {"region"}, "поручен": {"actionitem"}, "совещан": {"meeting"},
	"протокол": {"minutes"}, "замещен": {"substitution"}, "пользовател": {"user", "login"}, "вид": {"kind"}, "журнал": {"register"},
	"согласован": {"approval"}, "правил": {"rule"}, "доск": {"board"}, "карточк": {"ticket"}, "стать": {"article"}, "контакт": {"contact"},
	"наша": {"businessunit"}, "группа": {"group", "role"}, "дел": {"casefile"}, "номенклатур": {"casefile"}, "инструкц": {"memo", "document"},
	"служебн": {"memo"}, "записк": {"memo"}, "входящ": {"incoming"}, "исходящ": {"outgoing"}, "накладн": {"waybill"}, "упд": {"universaltransfer"},
}

func words(q string) []string {
	q = strings.ReplaceAll(strings.ToLower(q), "ё", "е")
	var out []string
	for _, w := range strings.FieldsFunc(q, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) < 2 {
			continue
		}
		matched := false
		for root, en := range ru {
			if strings.HasPrefix(w, root) {
				out = append(out, en...)
				matched = true
			}
		}
		if !matched {
			out = append(out, w)
		}
	}
	return out
}

// Find ищет наборы сущностей по словам запроса, русским или английским.
func (c *Catalog) Find(query string, limit int) []Set {
	ws := words(query)
	if len(ws) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = 15
	}
	type scored struct {
		s  Set
		sc int
	}
	var res []scored
	for _, s := range c.Sets {
		name := strings.ToLower(s.Name)
		sc := 0
		for _, w := range ws {
			if strings.Contains(name, w) {
				sc += 2
				// имя, которое почти целиком совпало со словом, важнее длинного составного
				if len(name) <= len(w)+3 {
					sc += 3
				}
			}
		}
		if sc > 0 {
			res = append(res, scored{s, sc})
		}
	}
	sort.Slice(res, func(i, j int) bool {
		if res[i].sc != res[j].sc {
			return res[i].sc > res[j].sc
		}
		if len(res[i].s.Name) != len(res[j].s.Name) {
			return len(res[i].s.Name) < len(res[j].s.Name)
		}
		return res[i].s.Name < res[j].s.Name
	})
	if len(res) > limit {
		res = res[:limit]
	}
	out := make([]Set, len(res))
	for i, r := range res {
		out[i] = r.s
	}
	return out
}

// Describe возвращает свойства и ссылки набора с учётом наследования.
func (c *Catalog) Describe(set string) (s Set, props, navs []Field, ok bool) {
	for _, x := range c.Sets {
		if strings.EqualFold(x.Name, set) {
			s, ok = x, true
			break
		}
	}
	if !ok {
		return
	}
	seen := map[string]bool{}
	for name := s.Type; name != "" && !seen[name]; {
		seen[name] = true
		t := c.types[name]
		if t == nil {
			break
		}
		props = append(props, t.Props...)
		navs = append(navs, t.Navs...)
		name = t.Base
	}
	sort.Slice(props, func(i, j int) bool { return props[i].Name < props[j].Name })
	sort.Slice(navs, func(i, j int) bool { return navs[i].Name < navs[j].Name })
	return
}
