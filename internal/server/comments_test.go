package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Комментарии карточки читаются действием LoadComments с типами карточки и доски,
// добавляются действием CreateComment.
func TestTicketComments(t *testing.T) {
	comments := []map[string]any{
		{"Id": 1, "EntityId": 5, "Text": "первый", "Status": "Active", "Time": "2026-09-29T08:51:19Z", "Author": map[string]any{"Id": 7, "Name": "Иванов И."}},
		{"Id": 2, "EntityId": 5, "Text": "удалённый", "Status": "Closed", "Time": "2026-09-29T09:00:00Z"},
	}
	var created map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/ITickets(5)"):
			json.NewEncoder(w).Encode(map[string]any{"Id": 5, "Name": "Карточка", "Uid": "AB-1", "BoardId": 9, "Status": "Active", "CommentsCount": len(comments)})
		case strings.HasSuffix(r.URL.Path, "/TeamsCommonAPI/LoadComments"):
			var p map[string]any
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &p)
			if p["entityGuid"] == "" || p["containerEntityGuid"] == "" || p["containerEntityId"] != float64(9) {
				http.Error(w, "нет типов", 500)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"value": comments})
		case strings.HasSuffix(r.URL.Path, "/TeamsCommonAPI/CreateComment"):
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &created)
			comments = append(comments, map[string]any{"Id": 3, "EntityId": 5, "Text": created["comment"], "Status": "Active", "Time": "2026-10-02T10:00:00Z"})
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cs := connect(t, true, srv.URL+"/odata")
	call := func(tool string, args map[string]any) string {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return res.Content[0].(*mcp.TextContent).Text
	}
	out := call("rx_ticket", map[string]any{"id": 5})
	if !strings.Contains(out, "Комментарии (1;") || !strings.Contains(out, "первый") || !strings.Contains(out, "Иванов И.") {
		t.Errorf("в карточке нет комментариев:\n%s", out)
	}
	if strings.Contains(out, "удалённый") {
		t.Error("показан удалённый комментарий")
	}
	out = call("rx_comment_ticket", map[string]any{"id": 5, "text": "  готово  "})
	if created["comment"] != "готово" || created["entityId"] != float64(5) || created["appId"] == nil {
		t.Errorf("в RX ушло не то: %v", created)
	}
	if !strings.Contains(out, "Комментарий добавлен к карточке AB-1") || !strings.Contains(out, "готово") {
		t.Errorf("ответ: %s", out)
	}
	if _, ok := toolNames(t, connect(t, false, srv.URL+"/odata"))["rx_comment_ticket"]; ok {
		t.Error("комментирование видно без RXMCP_ALLOW_WRITE")
	}
}
