package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/drxinfra/rxmcp/internal/odata"
	"github.com/drxinfra/rxmcp/internal/rx"
	"github.com/drxinfra/rxmcp/internal/server"
)

func TestWhatsNew(t *testing.T) {
	var filters []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		p := strings.TrimPrefix(r.URL.Path, "/odata/")
		f := r.URL.Query().Get("$filter")
		filters = append(filters, p+": "+f)
		switch p {
		case "IUsers":
			json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"Id": 7, "Name": "Иванов", "Login": map[string]any{"LoginName": "ivanov"}}}})
		case "IAssignments":
			if strings.Contains(f, "Created ge") {
				json.NewEncoder(w).Encode(map[string]any{"@odata.count": 1, "value": []any{map[string]any{"Id": 51, "Subject": "Согласовать договор", "Status": "InProcess", "Created": "2026-10-17T08:00:00Z"}}})
				return
			}
			if strings.Contains(f, "Deadline lt") {
				json.NewEncoder(w).Encode(map[string]any{"@odata.count": 1, "value": []any{map[string]any{"Id": 40, "Subject": "Отчёт за неделю", "Status": "InProcess", "Deadline": "2026-10-16T15:00:00Z"}}})
				return
			}
			w.WriteHeader(204)
		case "INotices":
			w.WriteHeader(204)
		case "ITasks":
			json.NewEncoder(w).Encode(map[string]any{"@odata.count": 1, "value": []any{map[string]any{"Id": 77, "Subject": "Поставка серверов", "Status": "Completed"}}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	cl, _ := odata.New(odata.Options{BaseURL: srv.URL + "/odata", Auth: "basic", Login: "ivanov", Password: "secret"})
	dir := t.TempDir()
	s := server.New(rx.New(cl, "ivanov", 0, 20, 100, rx.Formatter{Loc: time.UTC}), server.Options{Version: "test", MaxTextChars: 20000, StateDir: dir, StateKey: "rx.example:443-ivanov"})
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	s.MCP.Connect(ctx, st, nil)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(args map[string]any) string {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "rx_whats_new", Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%v %+v", err, res)
		}
		return res.Content[0].(*mcp.TextContent).Text
	}
	out := call(map[string]any{})
	for _, want := range []string{"за сутки: прошлого просмотра нет", "#51", "Согласовать договор", "Новых уведомлений нет", "#40", "Отчёт за неделю", "Поставка серверов", "сдвинута на сейчас"} {
		if !strings.Contains(out, want) {
			t.Errorf("нет %q в:\n%s", want, out)
		}
	}
	mark := filepath.Join(dir, "whatsnew-rx.example_443-ivanov.json")
	b, err := os.ReadFile(mark)
	if err != nil {
		t.Fatalf("отметка не сохранена: %v", err)
	}
	if out = call(map[string]any{"peek": true}); !strings.Contains(out, "с прошлого просмотра") || !strings.Contains(out, "не сдвинута") {
		t.Errorf("второй вызов:\n%s", out)
	}
	if b2, _ := os.ReadFile(mark); string(b2) != string(b) {
		t.Error("peek сдвинул отметку")
	}
	joined := strings.Join(filters, "\n")
	for _, want := range []string{"IAssignments: Performer/Id eq 7 and Created ge ", "INotices: Performer/Id eq 7 and Created ge ", "Status eq 'InProcess' and Deadline lt ", "ITasks: Author/Id eq 7 and Status eq 'Completed' and Modified ge "} {
		if !strings.Contains(joined, want) {
			t.Errorf("нет запроса %q среди:\n%s", want, joined)
		}
	}
	if _, err := call(map[string]any{"since": "3d"}), error(nil); err != nil {
		t.Fatal(err)
	}
}
