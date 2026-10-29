package rx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/drxinfra/rxmcp/internal/odata"
)

// Gate контрольная точка.
type Gate struct {
	ID          int64      `json:"Id"`
	Name        string     `json:"Name"`
	Description string     `json:"Description"`
	Note        string     `json:"Note"`
	Level       string     `json:"Level"`
	Assessment  string     `json:"Assessment"`
	IsPassed    bool       `json:"IsPassed"`
	PlanDate    *time.Time `json:"PlanDate"`
	ActualDate  *time.Time `json:"ActualDate"`
	Owner       *Ref       `json:"Owner"`
	Performer   *Ref       `json:"Performer"`
	Supervisor  *Ref       `json:"Supervisor"`
}

// Уровень контроля: в карточке КТ-0, КТ-1, КТ-2.
var gateLevels = []struct{ value, ru string }{
	{"Critical", "КТ-0"}, {"Key", "КТ-1"}, {"Operational", "КТ-2"},
}

// Прогноз по достижению.
var gateAssessments = []struct{ value, ru string }{
	{"OnTime", "в срок"}, {"MinorRisks", "есть несущественные риски"}, {"Threat", "под угрозой"},
}

// GateLevelRu «КТ-0» для Critical.
func GateLevelRu(v string) string {
	for _, l := range gateLevels {
		if l.value == v {
			return l.ru
		}
	}
	return v
}

// GateAssessmentRu подпись прогноза.
func GateAssessmentRu(v string) string {
	for _, a := range gateAssessments {
		if a.value == v {
			return a.ru
		}
	}
	return v
}

func parseGateLevel(s string) (string, error) {
	k := strings.NewReplacer("-", "", " ", "", "kt", "", "кт", "").Replace(normWord(s))
	switch k {
	case "":
		return "", nil
	case "0", "critical":
		return "Critical", nil
	case "1", "key":
		return "Key", nil
	case "2", "operational":
		return "Operational", nil
	}
	return "", fmt.Errorf("уровень контроля %q: КТ-0, КТ-1 или КТ-2", s)
}

func parseGateAssessment(s string) (string, error) {
	k := normWord(s)
	switch {
	case k == "":
		return "", nil
	case k == "ontime" || k == "в срок":
		return "OnTime", nil
	case k == "minorrisks" || strings.Contains(k, "несуществ"):
		return "MinorRisks", nil
	case k == "threat" || strings.Contains(k, "угроз"):
		return "Threat", nil
	}
	return "", fmt.Errorf("прогноз %q: в срок, есть несущественные риски или под угрозой", s)
}

// GateInput поля КТ. Пустое значит «не задано» (при правке: «не менять»).
type GateInput struct {
	Name        string
	Description string
	Note        string
	Level       string // КТ-0, КТ-1, КТ-2
	Assessment  string // прогноз
	PlanDate    *time.Time
	ActualDate  *time.Time
	Passed      *bool
	Owner       string // фамилия или Id
	Performer   string
	Supervisor  string
}

// dateValue дата без времени для полей-дат RX: полночь в том же смещении, в каком RX
// отдаёт даты этой записи (ref), иначе в UTC. Так дата не сдвигается на день.
func dateValue(d time.Time, ref *time.Time) string {
	loc := time.UTC
	if ref != nil {
		loc = ref.Location()
	}
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc).Format(time.RFC3339)
}

func (s *Service) gateBody(ctx context.Context, in GateInput, ref *time.Time, create bool) (map[string]any, error) {
	body := map[string]any{}
	if v := strings.TrimSpace(in.Name); v != "" {
		body["Name"] = v
	} else if create {
		return nil, errors.New("нужно название контрольной точки")
	}
	for k, v := range map[string]string{"Description": in.Description, "Note": in.Note} {
		if strings.TrimSpace(v) != "" {
			body[k] = strings.TrimSpace(v)
		}
	}
	lv, err := parseGateLevel(in.Level)
	if err != nil {
		return nil, err
	}
	if lv != "" {
		body["Level"] = lv
	}
	as, err := parseGateAssessment(in.Assessment)
	if err != nil {
		return nil, err
	}
	if as != "" {
		body["Assessment"] = as
	}
	if in.PlanDate != nil {
		body["PlanDate"] = dateValue(*in.PlanDate, ref)
	} else if create {
		return nil, errors.New("нужна плановая дата (plan_date)")
	}
	if in.ActualDate != nil {
		body["ActualDate"] = dateValue(*in.ActualDate, ref)
	}
	if in.Passed != nil {
		body["IsPassed"] = *in.Passed
	} else if create {
		body["IsPassed"] = false
	}
	for k, q := range map[string]string{"Owner": in.Owner, "Performer": in.Performer, "Supervisor": in.Supervisor} {
		if strings.TrimSpace(q) == "" {
			continue
		}
		id, err := s.oneEmployee(ctx, q)
		if err != nil {
			return nil, err
		}
		body[k] = map[string]any{"Id": id}
	}
	return body, nil
}

// CreatedGate итог создания одной КТ.
type CreatedGate struct {
	ID       int64
	Name     string
	PlanDate string
}

// CreateGates создаёт контрольные точки и добавляет их в карточку проекта. Ввод проверяется
// целиком до первой записи. Если КТ создалась, а в проект не добавилась, она удаляется:
// КТ без проекта никто не увидит.
func (s *Service) CreateGates(ctx context.Context, projectID int64, in []GateInput) ([]CreatedGate, error) {
	if len(in) == 0 {
		return nil, errors.New("нет контрольных точек для создания")
	}
	p, err := s.ProjectHead(ctx, projectID)
	if err != nil {
		return nil, err
	}
	ref := p.StartDate
	if ref == nil {
		ref = p.Modified
	}
	bodies := make([]map[string]any, 0, len(in))
	for i, g := range in {
		b, err := s.gateBody(ctx, g, ref, true)
		if err != nil {
			return nil, fmt.Errorf("КТ %d «%s»: %w", i+1, g.Name, err)
		}
		// Как в карточке: владелец по умолчанию руководитель проекта; ответственный
		// в RX обязателен, без него ставим того же руководителя.
		if p.Manager != nil {
			for _, k := range []string{"Owner", "Performer"} {
				if b[k] == nil {
					b[k] = map[string]any{"Id": p.Manager.ID}
				}
			}
		}
		bodies = append(bodies, b)
	}
	var out []CreatedGate
	for i, b := range bodies {
		data, err := s.c.Create(ctx, "IGates", b)
		if err != nil {
			return out, fmt.Errorf("КТ %d «%s» не создана: %w", i+1, b["Name"], err)
		}
		var created struct {
			ID int64 `json:"Id"`
		}
		if json.Unmarshal(data, &created) != nil || created.ID == 0 {
			return out, fmt.Errorf("КТ %d «%s» создана, но Id не разобран", i+1, b["Name"])
		}
		// Строка проекта повторяет поля КТ; RX и сам их перенесёт, но так строка
		// заполнена сразу, даже если обработчик изменения ссылки не сработает.
		link := map[string]any{"Gate": map[string]any{"Id": created.ID}, "PlanDate": b["PlanDate"], "IsPassed": b["IsPassed"]}
		for _, k := range []string{"Level", "Assessment", "Performer", "ActualDate"} {
			if v, ok := b[k]; ok {
				link[k] = v
			}
		}
		if _, err := s.c.Create(ctx, fmt.Sprintf("IProjectCores(%d)/GatesDirRX", p.ID), link); err != nil {
			msg := fmt.Sprintf("КТ %d «%s» создана (#%d), но в карточку проекта не добавилась: %v", i+1, b["Name"], created.ID, err)
			if derr := s.c.Delete(ctx, "IGates", created.ID); derr != nil {
				return out, fmt.Errorf("%s. Удалить её тоже не вышло (%v): удалите КТ #%d в RX вручную", msg, derr, created.ID)
			}
			return out, fmt.Errorf("%s. КТ удалена; если карточка проекта открыта на изменение, закройте её и повторите", msg)
		}
		out = append(out, CreatedGate{ID: created.ID, Name: fmt.Sprint(b["Name"]), PlanDate: fmt.Sprint(b["PlanDate"])})
	}
	return out, nil
}

// GateByID одна контрольная точка.
func (s *Service) GateByID(ctx context.Context, id int64) (*Gate, error) {
	var g Gate
	if err := s.c.Get(ctx, "IGates", id, odata.Query{
		Select: "Id,Name,Description,Note,Level,Assessment,IsPassed,PlanDate,ActualDate",
		Expand: "Owner($select=Id,Name),Performer($select=Id,Name),Supervisor($select=Id,Name)",
	}, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// UpdateGate меняет переданные поля КТ. Строку в карточке проекта RX обновит сам.
func (s *Service) UpdateGate(ctx context.Context, cur *Gate, in GateInput) ([]string, error) {
	ref := cur.PlanDate
	body, err := s.gateBody(ctx, in, ref, false)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, errors.New("нечего менять: передайте хотя бы одно поле")
	}
	if err := s.c.Patch(ctx, "IGates", cur.ID, body); err != nil {
		return nil, err
	}
	return fieldLabels(body, gateFieldRu), nil
}

var gateFieldRu = map[string]string{
	"Name": "название", "Description": "описание", "Note": "примечание", "Level": "уровень",
	"Assessment": "прогноз", "PlanDate": "дата (план)", "ActualDate": "дата (факт)", "IsPassed": "достигнута",
	"Owner": "владелец", "Performer": "ответственный", "Supervisor": "контролёр",
}
