package server_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/drxinfra/rxmcp/internal/odata"
	"github.com/drxinfra/rxmcp/internal/rx"
	"github.com/drxinfra/rxmcp/internal/server"
)

const helpTOC = `<html><head><title>Справка Directum RX 25.2</title></head><body><ul id="toc">
<li class="heading1" id="i1"><a class="heading1" href="admin.htm"><span>Администрирование</span></a><ul>
<li class="heading2" id="i1.1"><a class="heading2" href="substitution.htm"><span>Замещения</span></a></li>
<li class="heading2" id="i1.2"><a class="heading2" href="approval.htm"><span>Правила согласования</span></a></li>
</ul></li></ul></body></html>`

func helpPage(body string) []byte {
	return []byte(`<html><body><div id="idcontent"><!--ZOOMRESTART-->` + body + `<!--ZOOMSTOP--></div></body></html>`)
}

func connectHelp(t *testing.T, fetch func(context.Context, string) ([]byte, string, error)) *mcp.ClientSession {
	t.Helper()
	cl, err := odata.New(odata.Options{BaseURL: "http://127.0.0.1:1", Auth: "basic", Login: "ivanov", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	svc := rx.New(cl, "ivanov", 0, 20, 100, rx.Formatter{Loc: time.UTC})
	s := server.New(svc, server.Options{Version: "test", MaxTextChars: 20000,
		Help: &server.HelpSource{Path: filepath.Join(t.TempDir(), "help.idx.gz"), Base: "https://rx.example/Client/WebHelp/ru-RU", Fetch: fetch}})
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.MCP.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func callText(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcp.TextContent).Text
}

// Первый вопрос по справке запускает скачивание в фоне, следующий получает ответ.
func TestHelpToolsBackgroundIndex(t *testing.T) {
	pages := map[string][]byte{
		"hmcontent.htm":    []byte(helpTOC),
		"admin.htm":        helpPage(`<p>Раздел администратора.</p>`),
		"substitution.htm": helpPage(`<p>Замещение назначается на время отпуска сотрудника. См. <a href="approval.htm">правила согласования</a>.</p>`),
		"approval.htm":     helpPage(`<p>Правило согласования задаёт этапы.</p>`),
	}
	cs := connectHelp(t, func(_ context.Context, u string) ([]byte, string, error) {
		if b, ok := pages[u[strings.LastIndexByte(u, '/')+1:]]; ok {
			return b, u, nil
		}
		return nil, "", errors.New("404")
	})
	tools := toolNames(t, cs)
	for _, n := range []string{"rx_help_search", "rx_help_topic", "rx_help_toc"} {
		if tools[n] == nil || !tools[n].Annotations.ReadOnlyHint {
			t.Fatalf("нет инструмента %s или он не помечен как чтение", n)
		}
	}
	first := callText(t, cs, "rx_help_search", map[string]any{"query": "замещение на время отпуска"})
	if !strings.Contains(first, "скачивать") {
		t.Fatalf("первый вызов должен сообщить о скачивании: %s", first)
	}
	var out string
	for i := 0; i < 100; i++ {
		out = callText(t, cs, "rx_help_search", map[string]any{"query": "замещение на время отпуска"})
		if strings.Contains(out, "substitution.htm") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(out, "1. Замещения") || !strings.Contains(out, "раздел: Администрирование") {
		t.Fatalf("поиск: %s", out)
	}
	topic := callText(t, cs, "rx_help_topic", map[string]any{"topic": "substitution.htm"})
	for _, want := range []string{"Замещения", "https://rx.example/Client/WebHelp/ru-RU/substitution.htm", "[правила согласования](approval.htm)", "Справка Directum RX 25.2"} {
		if !strings.Contains(topic, want) {
			t.Errorf("в статье нет %q:\n%s", want, topic)
		}
	}
	toc := callText(t, cs, "rx_help_toc", map[string]any{"section": "администр", "depth": 1})
	if !strings.Contains(toc, "Замещения (substitution.htm)") || !strings.Contains(toc, "Правила согласования") {
		t.Errorf("оглавление: %s", toc)
	}
	if miss := callText(t, cs, "rx_help_topic", map[string]any{"topic": "none.htm"}); !strings.Contains(miss, "нет") {
		t.Errorf("несуществующая статья: %s", miss)
	}
}

// Справка недоступна: пользователь получает понятную причину и подсказку, остальной сервер работает.
func TestHelpUnavailable(t *testing.T) {
	cs := connectHelp(t, func(context.Context, string) ([]byte, string, error) { return nil, "", errors.New("нет связи") })
	callText(t, cs, "rx_help_search", map[string]any{"query": "замещение"})
	var out string
	for i := 0; i < 100; i++ {
		out = callText(t, cs, "rx_help_search", map[string]any{"query": "замещение"})
		if strings.Contains(out, "RXMCP_HELP_URL") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("нет подсказки про адрес справки: %s", out)
}
