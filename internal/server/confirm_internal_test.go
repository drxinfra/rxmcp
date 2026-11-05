package server

import (
	"fmt"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestConfirmationsTake(t *testing.T) {
	c := NewConfirmations()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	c.SetNow(func() time.Time { return now })
	call, other := [32]byte{1}, [32]byte{2}

	id := c.open("a", call)
	if id == "" || len(id) < 26 {
		t.Fatalf("ключ %q короче 128 бит", id)
	}
	if c.open("a", call) == id {
		t.Fatal("два ожидания с одним ключом")
	}
	for _, x := range []struct {
		id, owner string
		call      [32]byte
	}{{"нет-такого", "a", call}, {id, "b", call}, {id, "a", other}} {
		if got := c.take(x.id, x.owner, x.call); got != takeUnknown {
			t.Errorf("%q/%s: чужое или другое ожидание принято: %v", x.id, x.owner, got)
		}
	}
	if got := c.take(id, "a", call); got != takeOK {
		t.Fatalf("своё ожидание не принято: %v", got)
	}
	if got := c.take(id, "a", call); got != takeUsed {
		t.Fatalf("повтор того же ответа: %v", got)
	}
	// Принятый ответ помнится ещё confirmTTL, потом забывается.
	now = now.Add(confirmTTL - time.Second)
	if got := c.take(id, "a", call); got != takeUsed {
		t.Errorf("до срока повтор должен узнаваться: %v", got)
	}
	now = now.Add(confirmTTL)
	if got := c.take(id, "a", call); got != takeUnknown {
		t.Errorf("после срока запись должна уйти: %v", got)
	}

	// Ожидание без ответа истекает через confirmTTL.
	id = c.open("a", call)
	now = now.Add(confirmTTL)
	if got := c.take(id, "a", call); got != takeUnknown {
		t.Errorf("истёкшее ожидание принято: %v", got)
	}
	if len(c.entries) != 0 {
		t.Errorf("истёкшие записи не убраны: %d", len(c.entries))
	}
}

func TestConfirmationsBounded(t *testing.T) {
	c := NewConfirmations()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	c.SetNow(func() time.Time { return now })
	tick := func() { now = now.Add(time.Millisecond) }

	// Один владелец не держит больше confirmMaxOwner: вытесняется его самое старое.
	first := c.open("flood", [32]byte{})
	tick()
	other := c.open("victim", [32]byte{})
	tick()
	for range confirmMaxOwner + 10 {
		c.open("flood", [32]byte{})
		tick()
	}
	n := 0
	for _, e := range c.entries {
		if e.owner == "flood" {
			n++
		}
	}
	if n != confirmMaxOwner {
		t.Errorf("у одного владельца %d ожиданий, ждали %d", n, confirmMaxOwner)
	}
	if _, ok := c.entries[first]; ok {
		t.Error("самое старое ожидание владельца не вытеснено")
	}
	if _, ok := c.entries[other]; !ok {
		t.Error("ожидание другого владельца вытеснено")
	}

	// Всего не больше confirmMax.
	for i := range confirmMax + 50 {
		c.open(fmt.Sprintf("u%d", i), [32]byte{})
		tick()
	}
	if len(c.entries) > confirmMax {
		t.Errorf("в складе %d записей, предел %d", len(c.entries), confirmMax)
	}
}

func TestAccepted(t *testing.T) {
	for _, x := range []struct {
		res  *mcp.ElicitResult
		want bool
	}{
		{nil, false},
		{&mcp.ElicitResult{Action: "decline"}, false},
		{&mcp.ElicitResult{Action: "cancel"}, false},
		{&mcp.ElicitResult{Action: "accept"}, true},
		{&mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, true},
		{&mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": false}}, false},
		{&mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": "да"}}, false},
	} {
		if got := accepted(x.res); got != x.want {
			t.Errorf("%+v: %v, ждали %v", x.res, got, x.want)
		}
	}
}

func TestFormSupported(t *testing.T) {
	for _, x := range []struct {
		caps *mcp.ClientCapabilities
		want bool
	}{
		{nil, false},
		{&mcp.ClientCapabilities{}, false},
		{&mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{}}, true},
		{&mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{Form: &mcp.FormElicitationCapabilities{}}}, true},
		{&mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{URL: &mcp.URLElicitationCapabilities{}}}, false},
		{&mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{Form: &mcp.FormElicitationCapabilities{}, URL: &mcp.URLElicitationCapabilities{}}}, true},
	} {
		if got := formSupported(x.caps); got != x.want {
			t.Errorf("%+v: %v, ждали %v", x.caps, got, x.want)
		}
	}
}

// Отпечаток не зависит от порядка ключей и пробелов, но различает аргументы, инструмент и текст.
func TestConfirmCall(t *testing.T) {
	req := func(name, args string) *mcp.CallToolRequest {
		return &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: name, Arguments: []byte(args)}}
	}
	base := confirmCall(req("rx_abort_task", `{"id":9,"x":[1,2]}`), "Прекратить?")
	if confirmCall(req("rx_abort_task", ` { "x": [1, 2], "id": 9 } `), "Прекратить?") != base {
		t.Error("порядок ключей и пробелы меняют отпечаток")
	}
	for _, r := range []struct{ name, args, msg string }{
		{"rx_abort_task", `{"id":10,"x":[1,2]}`, "Прекратить?"},
		{"rx_complete_assignment", `{"id":9,"x":[1,2]}`, "Прекратить?"},
		{"rx_abort_task", `{"id":9,"x":[1,2]}`, "Прекратить задачу?"},
	} {
		if confirmCall(req(r.name, r.args), r.msg) == base {
			t.Errorf("%s %s %q: тот же отпечаток", r.name, r.args, r.msg)
		}
	}
}
