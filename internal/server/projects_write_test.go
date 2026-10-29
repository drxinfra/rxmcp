package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Риски и КТ через MCP: инструменты записи есть только с разрешением записи, пачка
// ограничена, вызов доходит до RX и отвечает Id созданных записей.
func TestProjectRisksAndGatesTools(t *testing.T) {
	var posts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := r.URL.Path[strings.Index(r.URL.Path, "/odata/")+len("/odata/"):]
		if r.Method == http.MethodPost {
			b, _ := io.ReadAll(r.Body)
			posts = append(posts, p+" "+string(b))
			json.NewEncoder(w).Encode(map[string]any{"Id": 40 + len(posts)})
			return
		}
		switch p {
		case "IProjectCores(2)":
			json.NewEncoder(w).Encode(map[string]any{"Id": 2, "Name": "Внедрение", "StartDate": "2026-05-06T00:00:00+03:00", "Manager": map[string]any{"Id": 268, "Name": "Руководитель"}})
		case "IPortfolioProgramRiskCategories":
			json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"Id": 2, "Name": "Ресурсный", "Status": "Active"}}})
		case "IPortfolioProgramProbabilities":
			json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"Id": 4, "Description": "Высокая", "PercentProbability": 75, "Status": "Active"}}})
		case "IPortfolioProgramImpacts":
			json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"Id": 3, "Description": "Средние", "Status": "Active"}}})
		case "IPortfolioProgramRisks":
			json.NewEncoder(w).Encode(map[string]any{"value": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ro := connect(t, false, srv.URL+"/odata")
	tools, err := ro.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools.Tools {
		if tl.Name == "rx_create_risks" || tl.Name == "rx_create_gates" {
			t.Errorf("%s без разрешения записи", tl.Name)
		}
	}
	if out := callText(t, ro, "rx_project_risks", map[string]any{"project_id": 2}); !strings.Contains(out, "рисков нет") || !strings.Contains(out, "вероятность: высокая 75% (#4)") {
		t.Errorf("rx_project_risks:\n%s", out)
	}

	cs := connect(t, true, srv.URL+"/odata")
	many := make([]any, 31)
	for i := range many {
		many[i] = map[string]any{"name": "r", "description": "d", "probability": "высокая", "impact": "средние"}
	}
	if out := callText(t, cs, "rx_create_risks", map[string]any{"project_id": 2, "risks": many}); !strings.Contains(out, "не больше 30") {
		t.Errorf("лимит пачки: %s", out)
	}
	out := callText(t, cs, "rx_create_risks", map[string]any{"project_id": 2, "risks": []any{
		map[string]any{"name": "Отпуска", "description": "Летом", "probability": "высокая", "impact": "средние", "category": "ресурсный"},
	}})
	if !strings.Contains(out, "Создано рисков: 1, проект «Внедрение» (#2):\n  #41 Отпуска") {
		t.Errorf("rx_create_risks: %s", out)
	}
	out = callText(t, cs, "rx_create_gates", map[string]any{"project_id": 2, "gates": []any{
		map[string]any{"name": "ТЗ утверждено", "plan_date": "20.11.2026", "level": "КТ-0"},
	}})
	if !strings.Contains(out, "Создано КТ: 1, добавлены в проект «Внедрение» (#2):\n  #42 ТЗ утверждено · план 20.11.2026") {
		t.Errorf("rx_create_gates: %s", out)
	}
	if len(posts) != 3 || !strings.HasPrefix(posts[2], `IProjectCores(2)/GatesDirRX {"Gate":{"Id":42}`) {
		t.Errorf("записи: %v", posts)
	}
	if out := callText(t, cs, "rx_create_gates", map[string]any{"project_id": 2, "gates": []any{map[string]any{"name": "Без даты", "plan_date": ""}}}); !strings.Contains(out, "нужна плановая дата") {
		t.Errorf("без даты: %s", out)
	}
}
