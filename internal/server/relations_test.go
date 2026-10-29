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

// Связи карточки как у RX 25.2: два графа, рёбра Source → Target с числовым LinkType.
func TestTicketRelations(t *testing.T) {
	node := func(id, board int64, uid, name, status string) map[string]any {
		return map[string]any{"Depth": 0, "TicketInfo": map[string]any{
			"Id": id, "BoardId": board, "Uid": uid, "Name": name, "Status": status}}
	}
	edge := func(s, t int64, typ int) map[string]any {
		return map[string]any{"Source": s, "Target": t, "LinkType": typ}
	}
	self := node(5, 9, "AB-1", "Эпик", "Active")
	rel5 := map[string]any{
		"Id": 5,
		"InboundGraph": map[string]any{"AccessDeniedNodeCount": 1,
			"Nodes": []any{self, node(3, 9, "AB-0", "Большой эпик", "Active"), node(4, 7, "XY-4", "Чужая доска", "Closed")},
			"Edges": []any{edge(3, 5, 2), edge(4, 5, 0)}},
		"OutboundGraph": map[string]any{"AccessDeniedNodeCount": 0,
			"Nodes": []any{self, node(6, 9, "AB-2", "Подзадача", "Active"), node(7, 9, "AB-3", "Дубль", "Deleted"), node(8, 9, "AB-4", "Соседняя", "Active")},
			"Edges": []any{edge(5, 6, 2), edge(5, 7, 1), edge(5, 8, 3)}},
	}
	empty := map[string]any{"Id": 8,
		"InboundGraph":  map[string]any{"Nodes": []any{node(8, 9, "AB-4", "Соседняя", "Active")}, "Edges": []any{}},
		"OutboundGraph": map[string]any{"Nodes": []any{}, "Edges": []any{}}}
	var batchBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/ITickets(5)"):
			json.NewEncoder(w).Encode(map[string]any{"Id": 5, "Name": "Эпик", "Uid": "AB-1", "BoardId": 9, "Status": "Active"})
		case strings.HasSuffix(r.URL.Path, "/AgileBoards/GetRelations(ticketId=5)"):
			json.NewEncoder(w).Encode(rel5)
		case strings.HasSuffix(r.URL.Path, "/AgileBoards/BatchGetRelations") && r.Method == http.MethodPost:
			b, _ := io.ReadAll(r.Body)
			batchBody = string(b)
			json.NewEncoder(w).Encode(map[string]any{"Relations": []any{empty, rel5}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cs := connect(t, false, srv.URL+"/odata")
	call := func(name string, args map[string]any) string {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return res.Content[0].(*mcp.TextContent).Text
	}
	out := call("rx_ticket", map[string]any{"id": 5})
	for _, want := range []string{
		"Связи (5,",
		"дочерняя для:\n    AB-0 (id 3)", "родитель для:\n    AB-2 (id 6)",
		"обязательна для:\n    XY-4 (id 4)", "доска #7",
		"дублируется:\n    AB-3 (id 7)", "связана с:\n    AB-4 (id 8)",
		"ещё 1 связанных карточек без доступа",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("нет %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "AB-2 (id 6) · активна · доска") {
		t.Errorf("карточка с той же доски помечена доской:\n%s", out)
	}
	if strings.Index(out, "дочерняя для") > strings.Index(out, "родитель для") {
		t.Errorf("родитель должен идти раньше дочерних:\n%s", out)
	}

	out = call("rx_ticket_relations", map[string]any{"ids": []int64{5, 8, 5}})
	if !strings.Contains(batchBody, `"ticketIds":[5,8]`) {
		t.Errorf("BatchGetRelations вызван не так: %s", batchBody)
	}
	if !strings.Contains(out, "Карточка AB-4 (id 8): Соседняя\nСвязей с другими карточками нет.") {
		t.Errorf("пустая карточка показана не так:\n%s", out)
	}
	if strings.Index(out, "AB-1 (id 5)") > strings.Index(out, "AB-4 (id 8)") {
		t.Errorf("порядок карточек не как в запросе:\n%s", out)
	}
}

// На версии RX без связей карточек GetRelations отвечает 404: карточка показывается без блока.
func TestTicketWithoutRelationsAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/ITickets(5)") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"Id": 5, "Name": "Эпик", "Uid": "AB-1", "BoardId": 9})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	cs := connect(t, false, srv.URL+"/odata")
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "rx_ticket", Arguments: map[string]any{"id": 5}})
	if err != nil {
		t.Fatal(err)
	}
	if out := res.Content[0].(*mcp.TextContent).Text; strings.Contains(out, "Связ") {
		t.Errorf("на 404 блок связей не нужен:\n%s", out)
	}
}
