package server_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/drxinfra/rxmcp/internal/odata"
	"github.com/drxinfra/rxmcp/internal/rx"
	"github.com/drxinfra/rxmcp/internal/server"
)

// Подключаемся к серверу как MCP-клиент через транспорт в памяти.
func connect(t *testing.T, allowWrite bool, rxURL string) *mcp.ClientSession {
	t.Helper()
	cl, err := odata.New(odata.Options{BaseURL: rxURL, Auth: "basic", Login: "ivanov", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	svc := rx.New(cl, "ivanov", 0, 20, 100, rx.Formatter{Loc: time.UTC})
	s := server.New(svc, server.Options{Version: "test", AllowWrite: allowWrite, MaxTextChars: 20000})
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := s.MCP.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := c.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func toolNames(t *testing.T, cs *mcp.ClientSession) map[string]*mcp.Tool {
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]*mcp.Tool{}
	for _, tl := range res.Tools {
		m[tl.Name] = tl
	}
	return m
}

func TestReadOnlyHidesWriteTools(t *testing.T) {
	cs := connect(t, false, "http://127.0.0.1:1/odata")
	tools := toolNames(t, cs)
	if _, ok := tools["rx_complete_assignment"]; ok {
		t.Error("инструмент записи виден без RXMCP_ALLOW_WRITE")
	}
	for _, n := range []string{"rx_whoami", "rx_my_assignments", "rx_get_assignment", "rx_get_task", "rx_list_tasks", "rx_find_documents", "rx_get_document", "rx_get_document_text", "rx_find_employees"} {
		tl, ok := tools[n]
		if !ok {
			t.Errorf("нет инструмента %s", n)
			continue
		}
		if tl.Annotations == nil || !tl.Annotations.ReadOnlyHint {
			t.Errorf("%s без readOnlyHint", n)
		}
	}
	cs2 := connect(t, true, "http://127.0.0.1:1/odata")
	tools2 := toolNames(t, cs2)
	for _, n := range []string{"rx_complete_assignment", "rx_create_simple_task", "rx_abort_task"} {
		tl, ok := tools2[n]
		if !ok {
			t.Errorf("с записью нет %s", n)
			continue
		}
		if tl.Annotations == nil || tl.Annotations.ReadOnlyHint {
			t.Errorf("%s помечен как readOnly", n)
		}
	}
	if tools2["rx_abort_task"].Annotations.DestructiveHint == nil || !*tools2["rx_abort_task"].Annotations.DestructiveHint {
		t.Error("abort должен быть destructive")
	}
	// Ошибка связи приходит как isError, а не как падение протокола.
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "rx_whoami", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "нет связи") {
		t.Errorf("ожидали isError с текстом про связь: %+v", res)
	}
	// Ресурсы и промпты объявлены.
	rt, err := cs.ListResourceTemplates(context.Background(), nil)
	if err != nil || len(rt.ResourceTemplates) != 3 {
		t.Errorf("templates: %v %d", err, len(rt.ResourceTemplates))
	}
	pr, err := cs.ListPrompts(context.Background(), nil)
	if err != nil || len(pr.Prompts) != 2 {
		t.Errorf("prompts: %v", err)
	}
	gp, err := cs.GetPrompt(context.Background(), &mcp.GetPromptParams{Name: "summarize_document", Arguments: map[string]string{"id": "300"}})
	if err != nil || !strings.Contains(gp.Messages[0].Content.(*mcp.TextContent).Text, "#300") {
		t.Errorf("prompt: %v", err)
	}
}

func TestBoardWriteToolsAndConfirm(t *testing.T) {
	tools := toolNames(t, connect(t, true, "http://127.0.0.1:1/odata"))
	for _, n := range []string{"rx_create_column", "rx_delete_tickets"} {
		if _, ok := tools[n]; !ok {
			t.Errorf("с записью нет %s", n)
		}
	}
	if d := tools["rx_delete_tickets"].Annotations.DestructiveHint; d == nil || !*d {
		t.Error("удаление карточек должно быть destructive")
	}
	if _, ok := toolNames(t, connect(t, false, "http://127.0.0.1:1/odata"))["rx_delete_tickets"]; ok {
		t.Error("удаление видно без RXMCP_ALLOW_WRITE")
	}
	args := map[string]any{"board": "2", "name": "Новая"}
	for _, proto := range protos {
		for _, noConfirm := range []bool{false, true} {
			// Клиент показывает форму и отвечает отказом.
			fc := &formClient{answer: &mcp.ElicitResult{Action: "decline"}}
			cs := dial(t, newWriteServer(t, writeServerOpts{RXURL: "http://127.0.0.1:1/odata", NoConfirm: noConfirm}), proto, fc.options(), nil)
			out := resultText(mustCall(t, cs, "rx_create_column", args, nil, ""))
			asked := len(fc.shown()) > 0
			// С формой отказ: в RX ничего не уходит.
			if !noConfirm && (!asked || !strings.Contains(out, "отменил")) {
				t.Errorf("%s: с формой ждали вопрос и отмену: asked=%v %q", proto, asked, out)
			}
			// RXMCP_CONFIRM=0: формы нет, запрос идёт в RX (фейка нет, отсюда ошибка связи).
			if noConfirm && (asked || !strings.Contains(out, "нет связи")) {
				t.Errorf("%s: без формы ждали запрос в RX: asked=%v %q", proto, asked, out)
			}
		}
	}
}

// У каждого инструмента в схеме есть properties-объект: Ollama с шаблонами на Jinja
// (GigaChat и другие GGUF) строит по схемам парсер и без properties отвечает 400 на весь запрос.
func TestInputSchemasHaveProperties(t *testing.T) {
	cs := connect(t, true, "http://127.0.0.1:1/odata")
	for name, tl := range toolNames(t, cs) {
		b, _ := json.Marshal(tl.InputSchema)
		var sc map[string]any
		if err := json.Unmarshal(b, &sc); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, ok := sc["properties"].(map[string]any); !ok {
			t.Errorf("%s: в схеме нет properties-объекта: %s", name, b)
		}
	}
}
