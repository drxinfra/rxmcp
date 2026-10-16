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

func TestUpdateNoticeOnce(t *testing.T) {
	rxs := fakeRX(t)
	cl, err := odata.New(odata.Options{BaseURL: rxs.URL + "/odata", Auth: "basic", Login: "ivanov", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	s := server.New(rx.New(cl, "ivanov", 0, 20, 100, rx.Formatter{Loc: time.UTC}), server.Options{Version: "v0.8.0", MaxTextChars: 20000})
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := s.MCP.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	call := func() string {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "rx_my_assignments", Arguments: map[string]any{}})
		if err != nil || res.IsError {
			t.Fatalf("%v %+v", err, res)
		}
		var b strings.Builder
		for _, c := range res.Content {
			b.WriteString(c.(*mcp.TextContent).Text)
		}
		return b.String()
	}
	if strings.Contains(call(), "Вышла rxmcp") {
		t.Fatal("новость без новой версии")
	}
	s.SetUpdate("v0.9.0")
	if txt := call(); !strings.Contains(txt, "Вышла rxmcp v0.9.0 (сейчас v0.8.0)") || !strings.Contains(txt, "rxmcp update") {
		t.Errorf("нет новости о версии: %s", txt)
	}
	if strings.Contains(call(), "Вышла rxmcp") {
		t.Error("новость повторилась во втором ответе")
	}
	// rx_whoami показывает версию и новость всегда, когда она есть.
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "rx_whoami", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("%v %+v", err, res)
	}
	if txt := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(txt, "rxmcp v0.8.0") || !strings.Contains(txt, "Вышла rxmcp v0.9.0") {
		t.Errorf("whoami: %s", txt)
	}
}
