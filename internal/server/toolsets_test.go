package server_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/drxinfra/rxmcp/internal/odata"
	"github.com/drxinfra/rxmcp/internal/rx"
	"github.com/drxinfra/rxmcp/internal/server"
)

func connectTools(t *testing.T, spec string) (*mcp.ClientSession, *mcp.InitializeResult) {
	t.Helper()
	tools, err := server.ParseTools(spec)
	if err != nil {
		t.Fatal(err)
	}
	cl, err := odata.New(odata.Options{BaseURL: "http://127.0.0.1:1/odata", Auth: "basic", Login: "ivanov", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	svc := rx.New(cl, "ivanov", 0, 20, 100, rx.Formatter{Loc: time.UTC})
	help := &server.HelpSource{Path: t.TempDir() + "/help.idx.gz", Base: "https://rx.example/Client/WebHelp/ru-RU",
		Fetch: func(context.Context, string) ([]byte, string, error) { return nil, "", context.Canceled }}
	s := server.New(svc, server.Options{Version: "test", AllowWrite: true, NoConfirm: true, MaxTextChars: 20000, OData: cl, Help: help, Tools: tools})
	ct, st := mcp.NewInMemoryTransports()
	if _, err := s.MCP.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, cs.InitializeResult()
}

// Новый инструмент должен попасть в какой-нибудь набор, иначе RXMCP_TOOLS его молча спрячет
// или, наоборот, не сможет убрать.
func TestEveryToolInToolset(t *testing.T) {
	cs, _ := connectTools(t, "")
	all := toolNames(t, cs)
	if len(all) < 40 {
		t.Fatalf("всего инструментов %d, ожидалось 40+ (запись, справка и каталог включены)", len(all))
	}
	for name := range all {
		if _, err := server.ParseTools(name); err != nil {
			t.Errorf("%s не входит ни в один набор RXMCP_TOOLS", name)
		}
	}
	// И обратно: в наборах нет имён, которых сервер не регистрирует (переименовали и забыли).
	every, err := server.ParseTools(strings.Join(server.ToolsetNames(), ","))
	if err != nil {
		t.Fatal(err)
	}
	for name := range every {
		if all[name] == nil {
			t.Errorf("%s есть в наборах, но сервер его не регистрирует", name)
		}
	}
	if len(every) != len(all) {
		t.Errorf("в наборах %d инструментов, у сервера %d", len(every), len(all))
	}
}

func TestToolsetFilters(t *testing.T) {
	cs, init := connectTools(t, "boards, rx_query")
	got := toolNames(t, cs)
	for _, n := range []string{"rx_whoami", "rx_my_assignments", "rx_complete_assignment", "rx_board", "rx_create_ticket", "rx_query"} {
		if got[n] == nil {
			t.Errorf("нет %s", n)
		}
	}
	for _, n := range []string{"rx_kb_search", "rx_help_search", "rx_projects", "rx_find_entity", "rx_call_action"} {
		if got[n] != nil {
			t.Errorf("%s виден, хотя набора нет в RXMCP_TOOLS", n)
		}
	}
	if len(got) > 27 {
		t.Errorf("инструментов %d, ожидалось не больше 27", len(got))
	}
	if strings.Contains(init.Instructions, "rx_help_search") {
		t.Error("инструкции зовут rx_help_search, которого нет в наборе")
	}
	if !strings.Contains(init.Instructions, "rx_whoami") {
		t.Error("из инструкций пропала общая часть")
	}
}

func TestParseTools(t *testing.T) {
	for _, spec := range []string{"", "all", "core,all"} {
		if m, err := server.ParseTools(spec); err != nil || m != nil {
			t.Errorf("%q: ожидались все инструменты, получено %v %v", spec, m, err)
		}
	}
	m, err := server.ParseTools("kb")
	if err != nil || !m["rx_whoami"] || !m["rx_kb_search"] || m["rx_board"] {
		t.Errorf("kb: %v %v", m, err)
	}
	if _, err := server.ParseTools("boards,wiki"); err == nil || !strings.Contains(err.Error(), "wiki") {
		t.Errorf("неизвестный набор принят: %v", err)
	}
}
