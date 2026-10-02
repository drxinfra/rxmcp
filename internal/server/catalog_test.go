package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/drxinfra/rxmcp/internal/odata"
	"github.com/drxinfra/rxmcp/internal/rx"
	"github.com/drxinfra/rxmcp/internal/server"
)

const testCSDL = `<?xml version="1.0" encoding="utf-8"?>
<edmx:Edmx Version="4.0" xmlns:edmx="http://docs.oasis-open.org/odata/ns/edmx"><edmx:DataServices>
<Schema Namespace="Sungero" xmlns="http://docs.oasis-open.org/odata/ns/edm">
 <EntityType Name="Contract"><Property Name="Id" Type="Edm.Int64"/><Property Name="Name" Type="Edm.String"/>
  <NavigationProperty Name="Counterparty" Type="Sungero.Company"/></EntityType>
 <EntityType Name="Company"><Property Name="Id" Type="Edm.Int64"/><Property Name="TIN" Type="Edm.String"/></EntityType>
 <EntityContainer Name="C"><EntitySet Name="IContracts" EntityType="Sungero.Contract"/><EntitySet Name="ICompanies" EntityType="Sungero.Company"/></EntityContainer>
</Schema></edmx:DataServices></edmx:Edmx>`

// поддельный сервис интеграции: метаданные, список договоров, один договор, одно действие
func fakeOData(t *testing.T, calls *[]string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		switch {
		case r.URL.Path == "/odata/$metadata":
			w.Header().Set("Content-Type", "application/xml")
			io.WriteString(w, testCSDL)
		case r.URL.Path == "/odata/IContracts":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"@odata.context":"x","@odata.count":2,"value":[{"Id":7,"Name":"Поставка"},{"Id":9,"Name":"Аренда"}]}`)
		case r.URL.Path == "/odata/IContracts(7)":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"@odata.context":"x", "Id":7, "Name":"Поставка"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/odata/Docflow/StartTask":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func connectCatalog(t *testing.T, base string, write bool) *mcp.ClientSession {
	t.Helper()
	cl, err := odata.New(odata.Options{BaseURL: base + "/odata", Auth: "basic", Login: "ivanov", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	svc := rx.New(cl, "ivanov", 0, 20, 100, rx.Formatter{Loc: time.UTC})
	s := server.New(svc, server.Options{Version: "test", AllowWrite: write, NoConfirm: true, MaxTextChars: 20000, OData: cl})
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

func TestCatalogTools(t *testing.T) {
	var calls []string
	srv := fakeOData(t, &calls)
	cs := connectCatalog(t, srv.URL, false)
	tools := toolNames(t, cs)
	for _, n := range []string{"rx_find_entity", "rx_describe_entity", "rx_query"} {
		if tools[n] == nil || !tools[n].Annotations.ReadOnlyHint {
			t.Fatalf("нет %s или он не помечен как чтение", n)
		}
	}
	if tools["rx_call_action"] != nil {
		t.Fatal("rx_call_action не должен быть виден без разрешения записи")
	}
	if out := callText(t, cs, "rx_find_entity", map[string]any{"query": "договоры"}); !strings.Contains(out, "IContracts: полей 2, ссылок 1") {
		t.Errorf("find: %s", out)
	}
	if out := callText(t, cs, "rx_describe_entity", map[string]any{"entity": "IContracts"}); !strings.Contains(out, "Name: String") || !strings.Contains(out, "Counterparty → Company") {
		t.Errorf("describe: %s", out)
	}
	out := callText(t, cs, "rx_query", map[string]any{"entity": "IContracts", "filter": "contains(Name,'а')", "select": "Id,Name", "top": 500})
	if !strings.Contains(out, "показано 2, всего по условию 2") || !strings.Contains(out, `{"Id":7,"Name":"Поставка"}`) {
		t.Errorf("query list: %s", out)
	}
	last := calls[len(calls)-1]
	if !strings.Contains(last, "%24top=100") || !strings.Contains(last, "%24count=true") || !strings.Contains(last, "%24select=Id%2CName") {
		t.Errorf("запрос к RX: %s", last)
	}
	if out := callText(t, cs, "rx_query", map[string]any{"entity": "IContracts", "id": 7}); !strings.Contains(out, `IContracts #7`) || strings.Contains(out, "odata.context") {
		t.Errorf("query one: %s", out)
	}
	if out := callText(t, cs, "rx_query", map[string]any{"entity": "IContracts(1)/../x"}); !strings.Contains(out, "имя набора") {
		t.Errorf("подозрительное имя набора должно отклоняться: %s", out)
	}
	// метаданные читаются один раз, а не на каждый вызов
	n := 0
	for _, c := range calls {
		if strings.Contains(c, "$metadata") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("$metadata запрошен %d раз", n)
	}
}

func TestCallActionOnlyWithWrite(t *testing.T) {
	var calls []string
	srv := fakeOData(t, &calls)
	cs := connectCatalog(t, srv.URL, true)
	if toolNames(t, cs)["rx_call_action"] == nil {
		t.Fatal("при разрешённой записи rx_call_action должен быть")
	}
	out := callText(t, cs, "rx_call_action", map[string]any{"action": "Docflow/StartTask", "params": map[string]any{"taskId": 5}})
	if !strings.Contains(out, "Docflow/StartTask выполнено") {
		t.Errorf("call: %s", out)
	}
	if bad := callText(t, cs, "rx_call_action", map[string]any{"action": "../x"}); !strings.Contains(bad, "формат") {
		t.Errorf("кривое имя действия: %s", bad)
	}
}
