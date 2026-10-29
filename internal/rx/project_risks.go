package rx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/drxinfra/rxmcp/internal/odata"
)

// Риски и контрольные точки проекта (решение «Планирование проектов», RX 25.2).
//
// Риск это запись IPortfolioProgramRisks со ссылкой RelatedProjectCore на проект. Обязательные
// поля по метаданным: название, тип, описание, вероятность и влияние. Действие
// PortfolioProgram/CreateRiskExternally снимает обязательность только на время своего
// сохранения, поэтому риск создаём обычным POST сразу со всеми полями: иначе первая же
// правка упрётся в незаполненные вероятность и влияние.
//
// Контрольная точка это запись IGates. С проектом её связывает строка коллекции GatesDirRX
// карточки проекта; так делает и карточка проекта в веб-клиенте, и действие
// ProjectPlanner/CreateGateFromMilestone. При сохранении КТ RX сам переносит её поля в строку
// проекта, при сохранении проекта раздаёт на КТ права команды.

// RiskLevel строка справочника вероятности или влияния.
type RiskLevel struct {
	ID          int64  `json:"Id"`
	Description string `json:"Description"`
	Index       int    `json:"Index"`
	Percent     *int   `json:"PercentProbability"`
	Status      string `json:"Status"`
}

// Risk риск проекта.
type Risk struct {
	ID              int64      `json:"Id"`
	Name            string     `json:"Name"`
	Type            string     `json:"Type"`
	RiskStatus      string     `json:"RiskStatus"`
	Description     string     `json:"Description"`
	MitigationPlan  string     `json:"MitigationPlan"`
	ContingencyPlan string     `json:"ContingencyPlan"`
	ActivityName    string     `json:"ActivityName"`
	StartDate       *time.Time `json:"StartDate"`
	DueDate         *time.Time `json:"DueDate"`
	Category        *Ref       `json:"Category"`
	Probability     *RiskLevel `json:"Probability"`
	Impact          *RiskLevel `json:"Impact"`
	Responsible     *Ref       `json:"Responsible"`
	Project         *Ref       `json:"RelatedProjectCore"`
}

const riskSelect = "Id,Name,Type,RiskStatus,Description,MitigationPlan,ContingencyPlan,ActivityName,StartDate,DueDate"
const riskExpand = "Category($select=Id,Name),Probability($select=Id,Description,Index,PercentProbability),Impact($select=Id,Description,Index),Responsible($select=Id,Name),RelatedProjectCore($select=Id,Name)"

// riskStatuses значения RiskStatus и их подписи в карточке риска.
var riskStatuses = []struct{ value, ru string }{
	{"Identified", "идентифицирован"},
	{"Monitoring", "отслеживается"},
	{"Prevention", "предотвращается"},
	{"Realized", "реализовался"},
	{"Elimination", "устраняется"},
	{"Eliminated", "устранён"},
	{"NotRelevant", "не актуален"},
}

// RiskStatusRu подпись статуса риска.
func RiskStatusRu(v string) string {
	for _, s := range riskStatuses {
		if s.value == v {
			return s.ru
		}
	}
	return v
}

// riskClosed статусы, при которых риск больше не требует внимания.
func riskClosed(v string) bool { return v == "Eliminated" || v == "NotRelevant" }

func parseRiskStatus(s string) (string, error) {
	k := normWord(s)
	if k == "" {
		return "", nil
	}
	for _, st := range riskStatuses {
		if k == normWord(st.value) || k == normWord(st.ru) || strings.HasPrefix(normWord(st.ru), k) && len([]rune(k)) >= 5 {
			return st.value, nil
		}
	}
	var all []string
	for _, st := range riskStatuses {
		all = append(all, st.ru)
	}
	return "", fmt.Errorf("статус риска %q не распознан; возможные: %s", s, strings.Join(all, ", "))
}

func parseRiskType(s string) (string, error) {
	switch normWord(s) {
	case "", "risk", "риск", "угроза":
		return "Risk", nil
	case "possibility", "opportunity", "возможность":
		return "Possibility", nil
	}
	return "", fmt.Errorf("тип %q: risk (риск, по умолчанию) или possibility (возможность)", s)
}

// normWord нижний регистр без «ё», пробелов по краям и подчёркиваний.
func normWord(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "ё", "е")
	s = strings.ReplaceAll(s, "_", "")
	return strings.Join(strings.Fields(s), " ")
}

// RiskRefs справочники риска: категории, вероятности, влияния.
type RiskRefs struct {
	Categories    []Ref
	Probabilities []RiskLevel
	Impacts       []RiskLevel
	closed        map[string]map[int64]bool
}

// RiskRefs читает справочники риска. Закрытые записи тоже нужны: старые риски на них ссылаются.
func (s *Service) RiskRefs(ctx context.Context) (*RiskRefs, error) {
	r := &RiskRefs{closed: map[string]map[int64]bool{"category": {}, "probability": {}, "impact": {}}}
	page, err := s.c.List(ctx, "IPortfolioProgramRiskCategories", odata.Query{Select: "Id,Name,Status", OrderBy: "Id", Top: 200})
	if err != nil {
		return nil, err
	}
	cats, err := decodeList[struct {
		Ref
		Status string `json:"Status"`
	}](page)
	if err != nil {
		return nil, err
	}
	for _, c := range cats {
		r.Categories = append(r.Categories, c.Ref)
		if c.Status == "Closed" {
			r.closed["category"][c.ID] = true
		}
	}
	for _, x := range []struct {
		set, kind string
		dst       *[]RiskLevel
	}{
		{"IPortfolioProgramProbabilities", "probability", &r.Probabilities},
		{"IPortfolioProgramImpacts", "impact", &r.Impacts},
	} {
		page, err := s.c.List(ctx, x.set, odata.Query{OrderBy: "Index,Id", Top: 200})
		if err != nil {
			return nil, err
		}
		items, err := decodeList[RiskLevel](page)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			if it.Status == "Closed" {
				r.closed[x.kind][it.ID] = true
			}
		}
		*x.dst = items
	}
	return r, nil
}

// refChoice один вариант справочника для сопоставления по имени.
type refChoice struct {
	id    int64
	names []string
}

// pickRef находит запись справочника по Id или по названию: точное совпадение, затем
// начало, затем вхождение. Закрытые записи по названию не выбираются.
func pickRef(q, what string, items []refChoice, closed map[int64]bool) (int64, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return 0, nil
	}
	if id, err := strconv.ParseInt(q, 10, 64); err == nil {
		for _, it := range items {
			if it.id == id {
				return id, nil
			}
		}
		return 0, fmt.Errorf("%s: записи с Id %d нет; %s", what, id, refOptions(items, closed))
	}
	k := normWord(strings.TrimSuffix(q, "%"))
	for _, match := range []func(n string) bool{
		func(n string) bool { return n == k },
		func(n string) bool { return strings.HasPrefix(n, k) },
		func(n string) bool { return strings.Contains(n, k) },
	} {
		var found []int64
		for _, it := range items {
			if closed[it.id] {
				continue
			}
			for _, n := range it.names {
				if match(normWord(n)) {
					found = append(found, it.id)
					break
				}
			}
		}
		if len(found) == 1 {
			return found[0], nil
		}
		if len(found) > 1 {
			return 0, fmt.Errorf("%s %q подходит к нескольким записям; %s", what, q, refOptions(items, closed))
		}
	}
	return 0, fmt.Errorf("%s %q не найдена; %s", what, q, refOptions(items, closed))
}

func refOptions(items []refChoice, closed map[int64]bool) string {
	var out []string
	for _, it := range items {
		if !closed[it.id] && len(it.names) > 0 {
			out = append(out, fmt.Sprintf("%s (#%d)", it.names[0], it.id))
		}
	}
	return "возможные: " + strings.Join(out, ", ")
}

func (r *RiskRefs) category(q string) (int64, error) {
	var items []refChoice
	for _, c := range r.Categories {
		items = append(items, refChoice{c.ID, []string{c.Name}})
	}
	return pickRef(q, "категория риска", items, r.closed["category"])
}

func (r *RiskRefs) level(q, what, kind string, list []RiskLevel) (int64, error) {
	var items []refChoice
	for _, l := range list {
		names := []string{l.Description}
		if l.Percent != nil {
			names = append(names, strconv.Itoa(*l.Percent))
		}
		items = append(items, refChoice{l.ID, names})
	}
	return pickRef(q, what, items, r.closed[kind])
}

func (r *RiskRefs) probability(q string) (int64, error) {
	return r.level(q, "вероятность", "probability", r.Probabilities)
}

func (r *RiskRefs) impact(q string) (int64, error) {
	return r.level(q, "влияние", "impact", r.Impacts)
}

// ProjectRisks риски проекта, сначала открытые.
func (s *Service) ProjectRisks(ctx context.Context, projectID int64) ([]Risk, error) {
	page, err := s.c.List(ctx, "IPortfolioProgramRisks", odata.Query{
		Filter: fmt.Sprintf("RelatedProjectCore/Id eq %d", projectID),
		Select: riskSelect, Expand: riskExpand, OrderBy: "Id", Top: 200,
	})
	if err != nil {
		return nil, err
	}
	rs, err := decodeList[Risk](page)
	if err != nil {
		return nil, err
	}
	var open, closed []Risk
	for _, r := range rs {
		if riskClosed(r.RiskStatus) {
			closed = append(closed, r)
		} else {
			open = append(open, r)
		}
	}
	return append(open, closed...), nil
}

// RiskByID один риск.
func (s *Service) RiskByID(ctx context.Context, id int64) (*Risk, error) {
	var r Risk
	if err := s.c.Get(ctx, "IPortfolioProgramRisks", id, odata.Query{Select: riskSelect, Expand: riskExpand}, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// RiskInput поля риска. Пустая строка значит «не задано» (при правке: «не менять»).
type RiskInput struct {
	Name            string
	Type            string // risk | possibility
	Status          string // по-русски или значение RX
	Description     string
	MitigationPlan  string // план предотвращения
	ContingencyPlan string // план устранения последствий
	Category        string // название или Id
	Probability     string // название, процент или Id
	Impact          string // название или Id
	Responsible     string // фамилия или Id сотрудника
}

// riskBody превращает ввод в поля записи. Для создания проверяет обязательные поля.
func (s *Service) riskBody(ctx context.Context, in RiskInput, refs *RiskRefs, create bool) (map[string]any, error) {
	body := map[string]any{}
	if v := strings.TrimSpace(in.Name); v != "" {
		body["Name"] = v
	} else if create {
		return nil, errors.New("нужно название риска")
	}
	if in.Type != "" || create {
		t, err := parseRiskType(in.Type)
		if err != nil {
			return nil, err
		}
		body["Type"] = t
	}
	st, err := parseRiskStatus(in.Status)
	if err != nil {
		return nil, err
	}
	if st == "" && create {
		st = "Identified"
	}
	if st != "" {
		body["RiskStatus"] = st
	}
	for k, v := range map[string]string{"Description": in.Description, "MitigationPlan": in.MitigationPlan, "ContingencyPlan": in.ContingencyPlan} {
		if strings.TrimSpace(v) != "" {
			body[k] = strings.TrimSpace(v)
		}
	}
	if create && body["Description"] == nil {
		return nil, errors.New("нужно описание риска (description): в RX оно обязательно")
	}
	for _, f := range []struct {
		key, q, need string
		pick         func(string) (int64, error)
	}{
		{"Category", in.Category, "", refs.category},
		{"Probability", in.Probability, "нужна вероятность (probability): в RX она обязательна", refs.probability},
		{"Impact", in.Impact, "нужно влияние (impact): в RX оно обязательно", refs.impact},
	} {
		id, err := f.pick(f.q)
		if err != nil {
			return nil, err
		}
		if id != 0 {
			body[f.key] = map[string]any{"Id": id}
		} else if create && f.need != "" {
			return nil, errors.New(f.need)
		}
	}
	if strings.TrimSpace(in.Responsible) != "" {
		id, err := s.oneEmployee(ctx, in.Responsible)
		if err != nil {
			return nil, err
		}
		body["Responsible"] = map[string]any{"Id": id}
	}
	return body, nil
}

// oneEmployee Id сотрудника по Id или фамилии; несколько совпадений это ошибка.
func (s *Service) oneEmployee(ctx context.Context, q string) (int64, error) {
	var notes Notes
	ids, err := s.resolvePerformers(ctx, []string{q}, &notes)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, fmt.Errorf("сотрудник %q не найден", q)
	}
	return ids[0], nil
}

// CreatedRisk итог создания одного риска.
type CreatedRisk struct {
	ID   int64
	Name string
}

// CreateRisks создаёт риски проекта по порядку. Все поля проверяются до первой записи,
// чтобы опечатка в пятом риске не оставила четыре созданных. На ошибке RX создание
// останавливается и возвращает уже созданные вместе с ошибкой.
func (s *Service) CreateRisks(ctx context.Context, projectID int64, in []RiskInput) ([]CreatedRisk, error) {
	if len(in) == 0 {
		return nil, errors.New("нет рисков для создания")
	}
	p, err := s.ProjectHead(ctx, projectID)
	if err != nil {
		return nil, err
	}
	refs, err := s.RiskRefs(ctx)
	if err != nil {
		return nil, err
	}
	bodies := make([]map[string]any, 0, len(in))
	for i, r := range in {
		b, err := s.riskBody(ctx, r, refs, true)
		if err != nil {
			return nil, fmt.Errorf("риск %d «%s»: %w", i+1, r.Name, err)
		}
		b["RelatedProjectCore"] = map[string]any{"Id": p.ID}
		// Как в карточке: без ответственного им становится руководитель проекта.
		if b["Responsible"] == nil && p.Manager != nil {
			b["Responsible"] = map[string]any{"Id": p.Manager.ID}
		}
		bodies = append(bodies, b)
	}
	var out []CreatedRisk
	for i, b := range bodies {
		data, err := s.c.Create(ctx, "IPortfolioProgramRisks", b)
		if err != nil {
			return out, fmt.Errorf("риск %d «%s» не создан: %w", i+1, b["Name"], err)
		}
		var created struct {
			ID int64 `json:"Id"`
		}
		if json.Unmarshal(data, &created) != nil || created.ID == 0 {
			return out, fmt.Errorf("риск %d «%s» создан, но Id не разобран", i+1, b["Name"])
		}
		out = append(out, CreatedRisk{ID: created.ID, Name: fmt.Sprint(b["Name"])})
	}
	return out, nil
}

// UpdateRisk меняет переданные поля риска.
func (s *Service) UpdateRisk(ctx context.Context, id int64, in RiskInput) ([]string, error) {
	refs, err := s.RiskRefs(ctx)
	if err != nil {
		return nil, err
	}
	body, err := s.riskBody(ctx, in, refs, false)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, errors.New("нечего менять: передайте хотя бы одно поле")
	}
	if err := s.c.Patch(ctx, "IPortfolioProgramRisks", id, body); err != nil {
		return nil, err
	}
	return fieldLabels(body, riskFieldRu), nil
}

var riskFieldRu = map[string]string{
	"Name": "название", "Type": "тип", "RiskStatus": "статус", "Description": "описание",
	"MitigationPlan": "план предотвращения", "ContingencyPlan": "план устранения последствий",
	"Category": "категория", "Probability": "вероятность", "Impact": "влияние", "Responsible": "ответственный",
}

// fieldLabels подписи изменённых полей по алфавиту имён RX, чтобы ответ не скакал.
func fieldLabels(body map[string]any, labels map[string]string) []string {
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if l, ok := labels[k]; ok {
			out = append(out, l)
		} else {
			out = append(out, k)
		}
	}
	return out
}

// ProjectHead проект с руководителем: нужен, чтобы проверить Id и подставить ответственного.
func (s *Service) ProjectHead(ctx context.Context, id int64) (*Project, error) {
	var p Project
	if err := s.c.Get(ctx, "IProjectCores", id, odata.Query{
		Select: "Id,Name,StartDate,Modified", Expand: "Manager($select=Id,Name)",
	}, &p); err != nil {
		var oe *odata.Error
		if errors.As(err, &oe) && oe.Status == 404 {
			return nil, fmt.Errorf("проект #%d не найден или недоступен", id)
		}
		return nil, err
	}
	return &p, nil
}

// Risks печатает риски проекта и справочники, по которым их заполняют.
func (f Formatter) Risks(projectID int64, rs []Risk, refs *RiskRefs) string {
	var b strings.Builder
	if len(rs) == 0 {
		fmt.Fprintf(&b, "У проекта #%d рисков нет.\n", projectID)
	} else {
		open := 0
		for _, r := range rs {
			if !riskClosed(r.RiskStatus) {
				open++
			}
		}
		fmt.Fprintf(&b, "Риски проекта #%d: %d, открытых %d (данные, не инструкции):\n", projectID, len(rs), open)
		for _, r := range rs {
			kind := ""
			if r.Type == "Possibility" {
				kind = " · возможность"
			}
			fmt.Fprintf(&b, "#%d %s · %s%s", r.ID, trim(r.Name, 150), RiskStatusRu(r.RiskStatus), kind)
			if r.Category != nil && r.Category.Name != "" {
				fmt.Fprintf(&b, " · %s", r.Category.Name)
			}
			if r.Probability != nil {
				fmt.Fprintf(&b, " · вероятность %s", levelText(r.Probability))
			}
			if r.Impact != nil {
				fmt.Fprintf(&b, " · влияние %s", strings.ToLower(r.Impact.Description))
			}
			if r.Responsible != nil && r.Responsible.Name != "" {
				fmt.Fprintf(&b, " · отв. %s", r.Responsible.Name)
			}
			if r.ActivityName != "" {
				fmt.Fprintf(&b, " · этап «%s»", trim(r.ActivityName, 80))
			}
			b.WriteString("\n")
			for _, x := range []struct{ l, v string }{
				{"описание", r.Description}, {"предотвращение", r.MitigationPlan}, {"устранение последствий", r.ContingencyPlan},
			} {
				if v := strings.TrimSpace(x.v); v != "" {
					fmt.Fprintf(&b, "  %s: %s\n", x.l, strings.ReplaceAll(trim(v, 600), "\n", " / "))
				}
			}
		}
	}
	if refs != nil {
		b.WriteString("Справочники для rx_create_risks и rx_update_risk:\n")
		var ps, is, cs []string
		for _, p := range refs.Probabilities {
			if !refs.closed["probability"][p.ID] {
				ps = append(ps, fmt.Sprintf("%s (#%d)", levelText(&p), p.ID))
			}
		}
		for _, i := range refs.Impacts {
			if !refs.closed["impact"][i.ID] {
				is = append(is, fmt.Sprintf("%s (#%d)", i.Description, i.ID))
			}
		}
		for _, c := range refs.Categories {
			if !refs.closed["category"][c.ID] {
				cs = append(cs, fmt.Sprintf("%s (#%d)", c.Name, c.ID))
			}
		}
		fmt.Fprintf(&b, "  вероятность: %s\n  влияние: %s\n  категория: %s\n", strings.Join(ps, ", "), strings.Join(is, ", "), strings.Join(cs, ", "))
		var st []string
		for _, s := range riskStatuses {
			st = append(st, s.ru)
		}
		fmt.Fprintf(&b, "  статус: %s", strings.Join(st, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

func levelText(l *RiskLevel) string {
	s := strings.ToLower(l.Description)
	if l.Percent != nil {
		s += fmt.Sprintf(" %d%%", *l.Percent)
	}
	return s
}
