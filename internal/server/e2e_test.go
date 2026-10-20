package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Небольшой фейк RX для сквозного вызова инструментов через MCP.
func fakeRX(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "ivanov" || p != "secret" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		p := strings.TrimPrefix(r.URL.Path, "/odata/")
		switch p {
		case "IUsers":
			json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"Id": 7, "Name": "Иванов Иван", "Login": map[string]any{"LoginName": "ivanov", "TypeAuthentication": "Password"}}}})
		case "IEmployees(7)":
			w.WriteHeader(404)
		case "IAssignments":
			json.NewEncoder(w).Encode(map[string]any{"@odata.count": 1, "value": []any{map[string]any{"Id": 5, "Subject": "Проверить", "Status": "InProcess", "Deadline": "2099-01-01T00:00:00Z", "Author": map[string]any{"Id": 1, "Name": "Автор"}}}})
		case "Docflow/CompleteAssignment":
			var m map[string]any
			json.NewDecoder(r.Body).Decode(&m)
			if m["result"] != "Complete" {
				w.WriteHeader(400)
				return
			}
			w.WriteHeader(204)
		case "IOfficialDocuments":
			w.WriteHeader(204) // среди официальных регламента нет
		case "IElectronicDocuments":
			if f := r.URL.Query().Get("$filter"); strings.Contains(f, "DocumentKind") || !strings.Contains(f, "'парол'") {
				w.WriteHeader(204)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"Id": 34, "Name": "Регламент смены паролей"}}})
		case "IAssignments(5)":
			json.NewEncoder(w).Encode(map[string]any{"@odata.type": "#X.Workflow.ISimpleAssignmentDto", "Id": 5, "Subject": "Проверить", "Status": "InProcess"})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestToolsEndToEnd(t *testing.T) {
	rxs := fakeRX(t)
	cs := connect(t, true, rxs.URL+"/odata")
	ctx := context.Background()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "rx_whoami", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("whoami: %v %+v", err, res)
	}
	if txt := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(txt, "Иванов Иван (id 7)") {
		t.Errorf("whoami: %s", txt)
	}
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "rx_my_assignments", Arguments: map[string]any{"limit": 5}})
	if err != nil || res.IsError {
		t.Fatalf("assignments: %v %+v", err, res)
	}
	if txt := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(txt, "#5 ● Проверить") {
		t.Errorf("assignments: %s", txt)
	}
	// Запись без elicitation у клиента: выполняется сразу.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "rx_complete_assignment", Arguments: map[string]any{"id": 5}})
	if err != nil || res.IsError {
		t.Fatalf("complete: %v %+v", err, res)
	}
	if txt := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(txt, "#5 (простое задание) завершено с результатом Complete") {
		t.Errorf("complete: %s", txt)
	}
	// Неверные аргументы отбиваются схемой.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "rx_get_assignment", Arguments: map[string]any{"id": "abc"}})
	if err == nil && (res == nil || !res.IsError) {
		t.Error("строка вместо id должна отклоняться")
	}
}

// Простой документ (регламент файлом) не находится среди официальных: поиск по названию
// повторяется среди всех документов без условия вида, и ответ об этом говорит.
func TestFindDocumentsFallsBackToAllTypes(t *testing.T) {
	cs := connect(t, false, fakeRX(t).URL+"/odata")
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "rx_find_documents",
		Arguments: map[string]any{"query": "паролей", "kind": "Регламент"}})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	txt := res.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{"найдено среди всех документов", "без условия вида «Регламент»", "#34 Регламент смены паролей"} {
		if !strings.Contains(txt, want) {
			t.Errorf("нет %q:\n%s", want, txt)
		}
	}
}
