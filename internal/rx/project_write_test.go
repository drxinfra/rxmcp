package rx_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drxinfra/rxmcp/internal/odata"
	"github.com/drxinfra/rxmcp/internal/rx"
)

// projRX имитирует справочники рисков, проект и запись рисков и КТ. Ответы повторяют
// RX 25.2: даты в смещении сервера (+04:00), закрытые записи справочников остаются в выборке.
type projRX struct {
	mu       sync.Mutex
	writes   []string         // «METHOD путь»
	bodies   []map[string]any // тела записей по порядку
	nextID   int64
	failLink bool
}

func (f *projRX) handle(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/Integration/odata/")
	if r.Method != http.MethodGet {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(body, &m)
		f.mu.Lock()
		f.writes = append(f.writes, r.Method+" "+p)
		f.bodies = append(f.bodies, m)
		f.nextID++
		id := f.nextID
		f.mu.Unlock()
		switch {
		case p == "IProjectCores(2)/GatesDirRX" && f.failLink:
			w.WriteHeader(400)
			w.Write([]byte(`{"error":{"message":"Карточка заблокирована пользователем Петров П."}}`))
		case r.Method == http.MethodPost:
			jsonOut(w, map[string]any{"Id": 500 + id})
		default:
			w.WriteHeader(204)
		}
		return
	}
	switch p {
	case "IProjectCores(2)":
		jsonOut(w, map[string]any{"Id": 2, "Name": "Внедрение", "StartDate": "2026-05-06T00:00:00+04:00", "Manager": map[string]any{"Id": 268, "Name": "Руководитель"}})
	case "IProjectCores(3)":
		w.WriteHeader(404)
	case "IPortfolioProgramRiskCategories":
		jsonOut(w, coll(
			map[string]any{"Id": 1, "Name": "Риск поставки", "Status": "Closed"},
			map[string]any{"Id": 2, "Name": "Ресурсный", "Status": "Active"},
			map[string]any{"Id": 7, "Name": "Технический", "Status": "Active"},
		))
	case "IPortfolioProgramProbabilities":
		jsonOut(w, coll(
			map[string]any{"Id": 1, "Description": "Низкая", "Index": 1, "PercentProbability": 25, "Status": "Active"},
			map[string]any{"Id": 2, "Description": "Средняя", "Index": 2, "PercentProbability": 50, "Status": "Active"},
			map[string]any{"Id": 3, "Description": "Вероятность возникновения и невозникновения равна", "Index": 3, "PercentProbability": 50, "Status": "Closed"},
			map[string]any{"Id": 4, "Description": "Высокая", "Index": 3, "PercentProbability": 75, "Status": "Active"},
		))
	case "IPortfolioProgramImpacts":
		jsonOut(w, coll(
			map[string]any{"Id": 1, "Description": "Минимальные", "Index": 1, "Status": "Active"},
			map[string]any{"Id": 3, "Description": "Средние", "Index": 3, "Status": "Active"},
		))
	case "IEmployees":
		jsonOut(w, coll(map[string]any{"Id": 9, "Name": "Петров Пётр", "Status": "Active"}))
	case "IPortfolioProgramRisks":
		jsonOut(w, coll(
			map[string]any{"Id": 11, "Name": "Старый", "RiskStatus": "Eliminated", "Type": "Risk"},
			map[string]any{"Id": 12, "Name": "Открытый", "RiskStatus": "Monitoring", "Type": "Risk", "Description": "Строка 1\nСтрока 2",
				"Probability": map[string]any{"Id": 4, "Description": "Высокая", "PercentProbability": 75}, "Impact": map[string]any{"Id": 3, "Description": "Средние"},
				"Category": map[string]any{"Id": 2, "Name": "Ресурсный"}, "Responsible": map[string]any{"Id": 268, "Name": "Руководитель"}},
		))
	case "IGates(70)":
		jsonOut(w, map[string]any{"Id": 70, "Name": "ТЗ утверждено", "PlanDate": "2026-11-20T00:00:00+04:00", "Level": "Key"})
	default:
		w.WriteHeader(404)
	}
}

func newProjSvc(t *testing.T) (*rx.Service, *projRX) {
	f := &projRX{}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	cl, err := odata.New(odata.Options{BaseURL: srv.URL + "/Integration/odata", Auth: "basic", Login: "ivanov", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Europe/Moscow")
	return rx.New(cl, "ivanov", 0, 20, 100, rx.Formatter{Loc: loc, Now: time.Now}), f
}

func ref(m map[string]any, k string) any {
	if v, ok := m[k].(map[string]any); ok {
		return v["Id"]
	}
	return nil
}

func TestCreateRisks(t *testing.T) {
	s, f := newProjSvc(t)
	got, err := s.CreateRisks(context.Background(), 2, []rx.RiskInput{
		{Name: "Отпуска команды", Description: "Летом", Probability: "высокая", Impact: "Средние", Category: "ресурсный"},
		{Name: "Нет доступа к стенду", Description: "Банк", Probability: "50%", Impact: "3", Responsible: "Петров", Status: "отслеживается", MitigationPlan: "Заявка заранее"},
	})
	if err != nil || len(got) != 2 {
		t.Fatalf("CreateRisks: %v %v", got, err)
	}
	if len(f.writes) != 2 || f.writes[0] != "POST IPortfolioProgramRisks" {
		t.Fatalf("записи: %v", f.writes)
	}
	a, b := f.bodies[0], f.bodies[1]
	for k, want := range map[string]any{"Probability": float64(4), "Impact": float64(3), "Category": float64(2), "Responsible": float64(268), "RelatedProjectCore": float64(2)} {
		if ref(a, k) != want {
			t.Errorf("первый риск, %s = %v, ждали %v", k, ref(a, k), want)
		}
	}
	if a["RiskStatus"] != "Identified" || a["Type"] != "Risk" || a["Description"] != "Летом" {
		t.Errorf("первый риск: %v", a)
	}
	// 50% подходит и к закрытой записи справочника, выбрать надо действующую.
	if ref(b, "Probability") != float64(2) || ref(b, "Responsible") != float64(9) || b["RiskStatus"] != "Monitoring" || b["MitigationPlan"] != "Заявка заранее" {
		t.Errorf("второй риск: %v", b)
	}
	if b["Category"] != nil {
		t.Errorf("категория не задана, а ушла: %v", b["Category"])
	}
}

func TestCreateRisksValidatesBeforeWriting(t *testing.T) {
	s, f := newProjSvc(t)
	for _, c := range []struct {
		in   []rx.RiskInput
		want string
	}{
		{[]rx.RiskInput{{Name: "A", Description: "d", Probability: "высокая", Impact: "средние"}, {Name: "B", Description: "d", Probability: "высокая"}}, "риск 2 «B»: нужно влияние"},
		{[]rx.RiskInput{{Name: "A", Probability: "высокая", Impact: "средние"}}, "нужно описание"},
		{[]rx.RiskInput{{Name: "A", Description: "d", Probability: "огромная", Impact: "средние"}}, "Высокая (#4)"},
		{[]rx.RiskInput{{Name: "A", Description: "d", Probability: "высокая", Impact: "средние", Category: "поставки"}}, "не найдена"}, // закрытая
		{[]rx.RiskInput{{Name: "A", Description: "d", Probability: "высокая", Impact: "средние", Status: "в работе"}}, "статус риска"},
	} {
		_, err := s.CreateRisks(context.Background(), 2, c.in)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("ждали ошибку с %q, получили %v", c.want, err)
		}
	}
	if len(f.writes) != 0 {
		t.Errorf("при ошибке ввода ничего не пишем: %v", f.writes)
	}
	if _, err := s.CreateRisks(context.Background(), 3, []rx.RiskInput{{Name: "A"}}); err == nil || !strings.Contains(err.Error(), "проект #3 не найден") {
		t.Errorf("чужой проект: %v", err)
	}
}

func TestUpdateRisk(t *testing.T) {
	s, f := newProjSvc(t)
	what, err := s.UpdateRisk(context.Background(), 12, rx.RiskInput{Status: "устранен", ContingencyPlan: "Перенос сроков"})
	if err != nil {
		t.Fatal(err)
	}
	if f.writes[0] != "PATCH IPortfolioProgramRisks(12)" || len(f.bodies[0]) != 2 || f.bodies[0]["RiskStatus"] != "Eliminated" {
		t.Errorf("правка: %v %v", f.writes, f.bodies)
	}
	if strings.Join(what, ", ") != "план устранения последствий, статус" {
		t.Errorf("что изменено: %v", what)
	}
	if _, err := s.UpdateRisk(context.Background(), 12, rx.RiskInput{}); err == nil {
		t.Error("пустая правка должна быть ошибкой")
	}
}

func TestProjectRisksOpenFirst(t *testing.T) {
	s, _ := newProjSvc(t)
	rs, err := s.ProjectRisks(context.Background(), 2)
	if err != nil || len(rs) != 2 || rs[0].ID != 12 {
		t.Fatalf("риски: %v %v", rs, err)
	}
	refs, err := s.RiskRefs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := s.F.Risks(2, rs, refs)
	for _, want := range []string{
		"Риски проекта #2: 2, открытых 1",
		"#12 Открытый · отслеживается · Ресурсный · вероятность высокая 75% · влияние средние · отв. Руководитель",
		"описание: Строка 1 / Строка 2",
		"#11 Старый · устранён",
		"вероятность: низкая 25% (#1), средняя 50% (#2), высокая 75% (#4)",
		"категория: Ресурсный (#2), Технический (#7)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("нет %q в\n%s", want, out)
		}
	}
}

func TestCreateGates(t *testing.T) {
	s, f := newProjSvc(t)
	d := time.Date(2026, 12, 25, 18, 0, 0, 0, time.FixedZone("MSK", 3*3600))
	got, err := s.CreateGates(context.Background(), 2, []rx.GateInput{
		{Name: "Пилот завершён", Description: "Замерен эффект", PlanDate: &d, Level: "КТ-1", Supervisor: "Петров"},
	})
	if err != nil || len(got) != 1 {
		t.Fatalf("CreateGates: %v %v", got, err)
	}
	if strings.Join(f.writes, "; ") != "POST IGates; POST IProjectCores(2)/GatesDirRX" {
		t.Fatalf("записи: %v", f.writes)
	}
	g, link := f.bodies[0], f.bodies[1]
	// Дата без сдвига, в смещении сервера, как RX отдаёт даты проекта.
	if g["PlanDate"] != "2026-12-25T00:00:00+04:00" || g["Level"] != "Key" || g["IsPassed"] != false {
		t.Errorf("КТ: %v", g)
	}
	if ref(g, "Owner") != float64(268) || ref(g, "Performer") != float64(268) || ref(g, "Supervisor") != float64(9) {
		t.Errorf("люди КТ: %v", g)
	}
	if ref(link, "Gate") != float64(got[0].ID) || link["PlanDate"] != g["PlanDate"] || ref(link, "Performer") != float64(268) {
		t.Errorf("строка проекта: %v", link)
	}
}

func TestCreateGatesRollsBackOrphan(t *testing.T) {
	s, f := newProjSvc(t)
	f.failLink = true
	d := time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC)
	_, err := s.CreateGates(context.Background(), 2, []rx.GateInput{{Name: "A", PlanDate: &d}, {Name: "B", PlanDate: &d}})
	if err == nil || !strings.Contains(err.Error(), "заблокирована") || !strings.Contains(err.Error(), "КТ удалена") {
		t.Fatalf("ошибка: %v", err)
	}
	if strings.Join(f.writes, "; ") != "POST IGates; POST IProjectCores(2)/GatesDirRX; DELETE IGates(501)" {
		t.Errorf("записи: %v", f.writes)
	}
	if _, err := s.CreateGates(context.Background(), 2, []rx.GateInput{{Name: "Без даты"}}); err == nil || !strings.Contains(err.Error(), "плановая дата") {
		t.Errorf("без даты: %v", err)
	}
}

func TestUpdateGate(t *testing.T) {
	s, f := newProjSvc(t)
	cur, err := s.GateByID(context.Background(), 70)
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	d := time.Date(2026, 11, 19, 18, 0, 0, 0, time.UTC)
	what, err := s.UpdateGate(context.Background(), cur, rx.GateInput{Passed: &yes, ActualDate: &d, Assessment: "в срок"})
	if err != nil {
		t.Fatal(err)
	}
	b := f.bodies[0]
	if f.writes[0] != "PATCH IGates(70)" || b["IsPassed"] != true || b["ActualDate"] != "2026-11-19T00:00:00+04:00" || b["Assessment"] != "OnTime" || len(b) != 3 {
		t.Errorf("правка КТ: %v %v", f.writes, b)
	}
	if strings.Join(what, ", ") != "дата (факт), прогноз, достигнута" {
		t.Errorf("что изменено: %v", what)
	}
}
