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

// История карточки читается функцией History/GetHistory с типом карточки; ответ как у RX 25.2:
// JSON строкой, записи «поле получило значение», колонка числом в ColunmId.
func TestTicketHistory(t *testing.T) {
	coll := func(action, who string) map[string]any {
		return map[string]any{"CollectionAction": action, "GroupCollectionProperties": []any{
			map[string]any{"ChildEntityId": 1, "ChildEntityDisplayValue": who, "CollectionProperties": []any{}}}}
	}
	rec := func(author int, date, typ, prop, label string, value any, display string) map[string]any {
		return map[string]any{"AuthorId": author, "Date": date, "DataType": typ, "PropertyName": prop,
			"LocalizedPropertyName": label, "Value": value, "DisplayValue": display}
	}
	created := "2026-10-19T09:56:21Z"
	inner := map[string]any{
		"Authors": []any{
			map[string]any{"Id": 59, "IsSystem": true, "Name": "Service User", "Thumbnail": nil},
			map[string]any{"Id": 7, "IsSystem": false, "Name": "Иванов И.", "Thumbnail": "/9j/AAAA"},
		},
		"HistoryRecords": []any{
			rec(59, "2026-10-20T12:43:25Z", "enum", "Status", "Состояние", "Closed", "Закрытый"),
			rec(7, "2026-10-20T12:43:23Z", "long", "ColunmId", "", 11, ""),
			rec(7, "2026-10-20T12:43:23Z", "collection", "TicketsTags", "Метки", coll("Added", "Выполнено"), ""),
			rec(7, "2026-10-19T10:04:00Z", "collection", "Performers", "Исполнители", coll("Removed", "Петров П."), ""),
			rec(7, "2026-10-19T10:04:00Z", "string", "Description", "Описание", "новый текст", ""),
			rec(7, "2026-10-19T10:04:00Z", "datetime", "Deadline", "Срок", "2026-10-24T18:00:00Z", ""),
			rec(7, created, "long", "ColunmId", "", 10, ""),
			rec(7, created, "string", "Uid", "Идентификатор", "AB-1", ""),
			rec(7, created, "string", "Name", "Заголовок", "Карточка", ""),
			rec(7, created, "integer", "CommentsCount", "Количество комментариев", 0, ""),
			rec(7, created, "double", "Laboriousness", "План", 6, ""),
			rec(7, created, "collection", "Performers", "Исполнители", coll("Added", "Петров П."), ""),
		},
	}
	innerJSON, _ := json.Marshal(inner)
	var historyPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/ITickets(5)"):
			json.NewEncoder(w).Encode(map[string]any{"Id": 5, "Name": "Карточка", "Uid": "AB-1", "BoardId": 9, "Status": "Closed"})
		case strings.Contains(r.URL.Path, "/History/GetHistory("):
			historyPath = r.URL.Path
			json.NewEncoder(w).Encode(map[string]any{"value": string(innerJSON)})
		case strings.HasSuffix(r.URL.Path, "/IColumns"):
			if !strings.Contains(r.URL.Query().Get("$filter"), "BoardId eq 9") {
				http.Error(w, "не та доска", 500)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"value": []any{
				map[string]any{"Id": 10, "Name": "В работе"}, map[string]any{"Id": 11, "Name": "Готово"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cs := connect(t, false, srv.URL+"/odata")
	call := func(args map[string]any) string {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "rx_ticket_history", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return res.Content[0].(*mcp.TextContent).Text
	}
	out := call(map[string]any{"id": 5})
	if !strings.Contains(historyPath, "entityId=5") || !strings.Contains(historyPath, "7197cc31-bf64-406e-8ead-0a7dde1c3c6b") {
		t.Errorf("GetHistory вызван не так: %s", historyPath)
	}
	for _, want := range []string{
		"История карточки AB-1 (id 5)", "Событий 4,",
		"19.10.2026 09:56 · Иванов И. · создал карточку", "колонка: В работе", "план: 6", "исполнители: + Петров П.",
		"исполнители: − Петров П.", "описание изменено", "срок: 24.10.2026 18:00",
		"20.10.2026 12:43 · Иванов И.", "колонка: Готово", "метки: + Выполнено",
		"20.10.2026 12:43 · система", "состояние: Закрытый",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("нет %q:\n%s", want, out)
		}
	}
	for _, noise := range []string{"AB-1\n", "количество комментариев", "заголовок", "/9j/"} {
		if strings.Contains(strings.ToLower(out[strings.Index(out, "\n"):]), strings.ToLower(noise)) {
			t.Errorf("лишнее %q в истории:\n%s", noise, out)
		}
	}
	if strings.Index(out, "В работе") > strings.Index(out, "Готово") {
		t.Errorf("события не от старых к новым:\n%s", out)
	}
	out = call(map[string]any{"id": 5, "limit": 1})
	if !strings.Contains(out, "показаны последние 1") || strings.Contains(out, "создал карточку") || !strings.Contains(out, "Закрытый") {
		t.Errorf("limit не оставил последнее событие:\n%s", out)
	}
}
