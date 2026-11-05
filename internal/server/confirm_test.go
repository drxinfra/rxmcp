package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/drxinfra/rxmcp/internal/odata"
	"github.com/drxinfra/rxmcp/internal/rx"
	"github.com/drxinfra/rxmcp/internal/server"
)

// Версии протокола в проверках подтверждения: на старой сервер спрашивает форму запросом
// посреди вызова, на 2026-07-28 отдаёт её в ответе инструмента (SEP-2322).
const (
	protoInCall    = "2025-06-18" // так, например, говорит Claude Code в VS Code
	protoRoundTrip = "2026-07-28" // клиент SDK по умолчанию
)

var protos = []string{protoInCall, protoRoundTrip}

const (
	declinedText = "Пользователь отменил действие. Ничего не изменено."
	// Задание #21 фейка newSubstRX: исполнитель Сидорова, пользователь её замещает.
	formFor21 = "Завершить простое задание #21 «Подготовить ответ» за Сидорова Анна? Исполнитель задания не вы, а Сидорова Анна: RX разрешает выполнять чужие задания только замещающему."
	doneFor21 = "Задание #21 (простое задание) выполнено за Сидорова Анна с результатом Complete."
	completes = "Docflow/CompleteAssignment"
)

var acceptAnswer = mcp.InputResponseMap{"confirm": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}}

// writeServerOpts сервер rxmcp с включённой записью на фейке RX.
type writeServerOpts struct {
	RXURL     string
	NoConfirm bool
	Confirms  *server.Confirmations
	Log       io.Writer // журнал сервера; nil = никуда
}

func newWriteServer(t *testing.T, o writeServerOpts) *server.Server {
	t.Helper()
	cl, err := odata.New(odata.Options{BaseURL: o.RXURL, Auth: "basic", Login: "ivanov", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	svc := rx.New(cl, "ivanov", 0, 20, 100, rx.Formatter{Loc: time.UTC})
	return server.New(svc, server.Options{Version: "test", AllowWrite: true, NoConfirm: o.NoConfirm, MaxTextChars: 20000,
		Logger: slog.New(slog.NewTextHandler(o.Log, nil)), Confirmations: o.Confirms})
}

// formClient клиент, который показывает форму подтверждения: отвечает answer и запоминает,
// какие формы ему показали.
type formClient struct {
	answer *mcp.ElicitResult
	manual bool // без автоповтора SDK на 2026-07-28: повторы с ответом делает сам тест

	mu    sync.Mutex
	forms []*mcp.ElicitParams
}

func (f *formClient) options() *mcp.ClientOptions {
	o := &mcp.ClientOptions{ElicitationHandler: func(_ context.Context, req *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.forms = append(f.forms, req.Params)
		a := *f.answer
		a.Content = maps.Clone(f.answer.Content)
		return &a, nil
	}}
	if f.manual {
		o.MultiRoundTrip = &mcp.MultiRoundTripOptions{Disabled: true}
	}
	return o
}

func (f *formClient) shown() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, p := range f.forms {
		out = append(out, p.Message)
	}
	return out
}

// dial подключает клиента SDK к серверу на версии протокола proto. Клиент до 2026-07-28
// изображает обёртка транспорта: server/discover к серверу не пускает (старые клиенты его
// не знают, клиент SDK откатывается на initialize) и ставит в initialize версию proto.
// wire, если задан, записывает всё, что прислал сервер.
func dial(t *testing.T, s *server.Server, proto string, opts *mcp.ClientOptions, wire *wireLog) *mcp.ClientSession {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := s.MCP.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	tr := &clientWire{Transport: ct, log: wire}
	if proto < protoRoundTrip {
		tr.version = proto
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, opts).Connect(ctx, tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	if got := cs.InitializeResult().ProtocolVersion; got != proto {
		t.Fatalf("сеанс на %s, ждали %s", got, proto)
	}
	return cs
}

// clientWire клиентский транспорт для проверок: см. dial.
type clientWire struct {
	mcp.Transport
	version string // не пусто: изображать клиента этой версии
	log     *wireLog
}

type wireMsg struct {
	m   jsonrpc.Message
	err error
}

func (w *clientWire) Connect(ctx context.Context) (mcp.Connection, error) {
	c, err := w.Transport.Connect(ctx)
	if err != nil {
		return nil, err
	}
	wc := &wireConn{Connection: c, version: w.version, log: w.log, in: make(chan wireMsg, 16)}
	go func() {
		for {
			m, err := c.Read(context.Background())
			if err == nil && wc.log != nil {
				wc.log.add(m)
			}
			wc.in <- wireMsg{m, err}
			if err != nil {
				return
			}
		}
	}()
	return wc, nil
}

type wireConn struct {
	mcp.Connection
	version string
	log     *wireLog
	in      chan wireMsg
}

func (c *wireConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	select {
	case r := <-c.in:
		return r.m, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *wireConn) Write(ctx context.Context, m jsonrpc.Message) error {
	if r, ok := m.(*jsonrpc.Request); ok && c.version != "" {
		switch r.Method {
		case "server/discover":
			c.in <- wireMsg{m: &jsonrpc.Response{ID: r.ID, Error: &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "method not found"}}}
			return nil
		case "initialize":
			var p map[string]any
			if err := json.Unmarshal(r.Params, &p); err != nil {
				return err
			}
			p["protocolVersion"] = c.version
			b, err := json.Marshal(p)
			if err != nil {
				return err
			}
			r2 := *r
			r2.Params = b
			m = &r2
		}
	}
	return c.Connection.Write(ctx, m)
}

// wireLog сообщения сервера, как они пришли клиенту.
type wireLog struct {
	mu   sync.Mutex
	msgs []jsonrpc.Message
}

func (w *wireLog) add(m jsonrpc.Message) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.msgs = append(w.msgs, m)
}

// elicitations параметры запросов elicitation/create.
func (w *wireLog) elicitations() []json.RawMessage {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []json.RawMessage
	for _, m := range w.msgs {
		if r, ok := m.(*jsonrpc.Request); ok && r.Method == "elicitation/create" {
			out = append(out, r.Params)
		}
	}
	return out
}

// results результаты ответов сервера.
func (w *wireLog) results() []json.RawMessage {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []json.RawMessage
	for _, m := range w.msgs {
		if r, ok := m.(*jsonrpc.Response); ok && r.Error == nil {
			out = append(out, r.Result)
		}
	}
	return out
}

// syncBuf журнал сервера в памяти: пишут разные горутины.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// callTool вызов инструмента; ответы на форму и requestState для повтора вручную.
func callTool(cs *mcp.ClientSession, tool string, args map[string]any, answers mcp.InputResponseMap, state string) (*mcp.CallToolResult, error) {
	return cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args, InputResponses: answers, RequestState: state})
}

func mustCall(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any, answers mcp.InputResponseMap, state string) *mcp.CallToolResult {
	t.Helper()
	res, err := callTool(cs, tool, args, answers, state)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// resultText текст ответа инструмента; пусто, если сервер вместо ответа прислал форму.
func resultText(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// askedForm форма подтверждения из ответа на 2026-07-28; nil, если ответ не форма.
func askedForm(res *mcp.CallToolResult) *mcp.ElicitParams {
	if !res.NeedsInput() {
		return nil
	}
	p, _ := res.InputRequests["confirm"].(*mcp.ElicitParams)
	return p
}

// На обоих путях одна форма и одно поведение: согласие выполняет задание ровно один раз,
// отказ, закрытая форма и снятая галочка ничего не меняют и отвечают одинаково.
func TestConfirmBothProtocols(t *testing.T) {
	for _, proto := range protos {
		for _, x := range []struct {
			name   string
			answer *mcp.ElicitResult
			out    string
			writes int
		}{
			{"accept", &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, doneFor21, 1},
			{"decline", &mcp.ElicitResult{Action: "decline"}, declinedText, 0},
			{"cancel", &mcp.ElicitResult{Action: "cancel"}, declinedText, 0},
			{"unchecked", &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": false}}, declinedText, 0},
		} {
			t.Run(proto+"/"+x.name, func(t *testing.T) {
				srv, f := newSubstRX(t, 0)
				fc := &formClient{answer: x.answer}
				cs := dial(t, newWriteServer(t, writeServerOpts{RXURL: srv.URL + "/odata"}), proto, fc.options(), nil)
				res := mustCall(t, cs, "rx_complete_assignment", map[string]any{"id": 21}, nil, "")
				if res.NeedsInput() || res.IsError || resultText(res) != x.out {
					t.Errorf("ответ: %q (isError %v, форма %v)", resultText(res), res.IsError, res.NeedsInput())
				}
				if got := fc.shown(); len(got) != 1 || got[0] != formFor21 {
					t.Errorf("формы: %q", got)
				}
				if n := f.count(completes); n != x.writes {
					t.Errorf("в RX ушло выполнений: %d, ждали %d", n, x.writes)
				}
			})
		}
	}
}

// Форма на проводе одна и та же: на старом протоколе это параметры elicitation/create, на
// 2026-07-28 тот же объект в inputRequests ответа, у которого нет текста и есть requestState.
func TestConfirmSameFormOnWire(t *testing.T) {
	want := `{"mode":"form","message":"` + formFor21 + `","requestedSchema":{"properties":{"confirm":{"description":"Да, выполнить","title":"Подтверждаю","type":"boolean"}},"required":["confirm"],"type":"object"}}`
	for _, proto := range protos {
		srv, _ := newSubstRX(t, 0)
		wire := &wireLog{}
		fc := &formClient{answer: &mcp.ElicitResult{Action: "decline"}}
		cs := dial(t, newWriteServer(t, writeServerOpts{RXURL: srv.URL + "/odata"}), proto, fc.options(), wire)
		mustCall(t, cs, "rx_complete_assignment", map[string]any{"id": 21}, nil, "")
		var got []string
		if proto == protoInCall {
			for _, p := range wire.elicitations() {
				got = append(got, string(p))
			}
		} else {
			for _, r := range wire.results() {
				var res struct {
					ResultType    string          `json:"resultType"`
					Content       json.RawMessage `json:"content"`
					RequestState  string          `json:"requestState"`
					InputRequests map[string]struct {
						Method string          `json:"method"`
						Params json.RawMessage `json:"params"`
					} `json:"inputRequests"`
				}
				if json.Unmarshal(r, &res) != nil || res.InputRequests == nil {
					continue
				}
				ir := res.InputRequests["confirm"]
				if res.ResultType != "input_required" || ir.Method != "elicitation/create" || len(res.InputRequests) != 1 ||
					res.RequestState == "" || (len(res.Content) > 0 && string(res.Content) != "null") {
					t.Errorf("ответ с формой не по SEP-2322: %s", r)
				}
				got = append(got, string(ir.Params))
			}
		}
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s: форма на проводе:\n%q\nждали:\n%s", proto, got, want)
		}
	}
}

// Протокол 2026-07-28, клиент сам повторяет вызов. Без ответа, с ответом без requestState,
// по погашенному или истёкшему requestState действие не выполняется.
func TestConfirmRoundTripNoAnswerOrExpired(t *testing.T) {
	srv, f := newSubstRX(t, 0)
	store := server.NewConfirmations()
	var mu sync.Mutex
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	store.SetNow(func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	fc := &formClient{answer: &mcp.ElicitResult{Action: "accept"}, manual: true}
	cs := dial(t, newWriteServer(t, writeServerOpts{RXURL: srv.URL + "/odata", Confirms: store}), protoRoundTrip, fc.options(), nil)
	args := map[string]any{"id": 21}

	first := mustCall(t, cs, "rx_complete_assignment", args, nil, "")
	if p := askedForm(first); p == nil || p.Message != formFor21 || first.RequestState == "" {
		t.Fatalf("первый заход не форма: %+v", first)
	}
	// Повтор без ответа: снова форма, с новым requestState.
	again := mustCall(t, cs, "rx_complete_assignment", args, nil, first.RequestState)
	if askedForm(again) == nil || again.RequestState == first.RequestState {
		t.Errorf("повтор без ответа: %+v", again)
	}
	// Ответ без requestState сервер не принимает: форма.
	if res := mustCall(t, cs, "rx_complete_assignment", args, acceptAnswer, ""); askedForm(res) == nil {
		t.Errorf("ответ без requestState принят: %q", resultText(res))
	}
	// Первый requestState погашен повтором без ответа: согласие по нему уже не действует.
	if res := mustCall(t, cs, "rx_complete_assignment", args, acceptAnswer, first.RequestState); !res.IsError || !strings.Contains(resultText(res), "уже обработан") {
		t.Errorf("ответ по погашенному requestState: %q", resultText(res))
	}
	// Ответ пришёл позже срока: новая форма.
	mu.Lock()
	now = now.Add(server.ConfirmTTL + time.Second)
	mu.Unlock()
	late := mustCall(t, cs, "rx_complete_assignment", args, acceptAnswer, again.RequestState)
	if askedForm(late) == nil {
		t.Fatalf("ответ после срока принят: %q", resultText(late))
	}
	if n := f.count(completes); n != 0 {
		t.Fatalf("без действующего согласия в RX ушло выполнений: %d", n)
	}
	// По свежей форме согласие выполняет задание.
	if res := mustCall(t, cs, "rx_complete_assignment", args, acceptAnswer, late.RequestState); res.IsError || resultText(res) != doneFor21 {
		t.Errorf("согласие по свежей форме: %q", resultText(res))
	}
	if n := f.count(completes); n != 1 {
		t.Errorf("выполнений: %d", n)
	}
	if got := fc.shown(); len(got) != 0 {
		t.Errorf("при ручных повторах SDK не должен сам показывать форму: %q", got)
	}
}

// Один ответ выполняет действие один раз, даже если клиент повторил его много раз, в том числе одновременно.
func TestConfirmRoundTripExactlyOnce(t *testing.T) {
	srv, f := newSubstRX(t, 0)
	fc := &formClient{answer: &mcp.ElicitResult{Action: "accept"}, manual: true}
	cs := dial(t, newWriteServer(t, writeServerOpts{RXURL: srv.URL + "/odata"}), protoRoundTrip, fc.options(), nil)
	args := map[string]any{"id": 21}
	first := mustCall(t, cs, "rx_complete_assignment", args, nil, "")
	if askedForm(first) == nil {
		t.Fatalf("первый заход не форма: %q", resultText(first))
	}
	outs := make([]*mcp.CallToolResult, 8)
	errs := make([]error, len(outs))
	var wg sync.WaitGroup
	for i := range outs {
		wg.Go(func() {
			outs[i], errs[i] = callTool(cs, "rx_complete_assignment", args, acceptAnswer, first.RequestState)
		})
	}
	wg.Wait()
	done, used := 0, 0
	for i, res := range outs {
		switch {
		case errs[i] != nil:
			t.Fatal(errs[i])
		case !res.IsError && resultText(res) == doneFor21:
			done++
		case res.IsError && strings.Contains(resultText(res), "второй раз действие не выполняется"):
			used++
		default:
			t.Errorf("ответ %d: %q", i, resultText(res))
		}
	}
	if done != 1 || used != len(outs)-1 {
		t.Errorf("выполнено %d, отказано как повтор %d", done, used)
	}
	if res := mustCall(t, cs, "rx_complete_assignment", args, acceptAnswer, first.RequestState); !res.IsError {
		t.Errorf("поздний повтор: %q", resultText(res))
	}
	if n := f.count(completes); n != 1 {
		t.Errorf("в RX ушло выполнений: %d", n)
	}
}

// Ответ из другого сеанса или к другому вызову действие не подтверждает: сервер спрашивает заново.
func TestConfirmRoundTripForeignState(t *testing.T) {
	srv, f := newSubstRX(t, 0)
	s := newWriteServer(t, writeServerOpts{RXURL: srv.URL + "/odata"})
	opts := (&formClient{answer: &mcp.ElicitResult{Action: "accept"}, manual: true}).options()
	a, b := dial(t, s, protoRoundTrip, opts, nil), dial(t, s, protoRoundTrip, opts, nil)
	first := mustCall(t, a, "rx_complete_assignment", map[string]any{"id": 21}, nil, "")
	if askedForm(first) == nil {
		t.Fatalf("первый заход не форма: %q", resultText(first))
	}
	if res := mustCall(t, b, "rx_complete_assignment", map[string]any{"id": 21}, acceptAnswer, first.RequestState); askedForm(res) == nil {
		t.Errorf("чужой сеанс подтвердил действие: %q", resultText(res))
	}
	res := mustCall(t, a, "rx_complete_assignment", map[string]any{"id": 22}, acceptAnswer, first.RequestState)
	if p := askedForm(res); p == nil || p.Message != "Завершить простое задание #22 «Своё задание»?" {
		t.Errorf("согласие на #21 выполнило #22: %q", resultText(res))
	}
	if n := f.count(completes); n != 0 {
		t.Fatalf("в RX ушло выполнений: %d", n)
	}
	// Свой сеанс, тот же вызов: выполняется.
	if res := mustCall(t, a, "rx_complete_assignment", map[string]any{"id": 21}, acceptAnswer, first.RequestState); resultText(res) != doneFor21 {
		t.Errorf("свой ответ: %q", resultText(res))
	}
	if n := f.count(completes); n != 1 {
		t.Errorf("выполнений: %d", n)
	}
}

// Клиент без формы (или только с url-формой): запись идёт без подтверждения формой, как
// раньше, а журнал один раз за сеанс говорит, почему формы не будет.
func TestConfirmFallbackWithoutForm(t *testing.T) {
	urlOnly := func() *mcp.ClientOptions {
		fc := &formClient{answer: &mcp.ElicitResult{Action: "decline"}}
		o := fc.options()
		o.Capabilities = &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{URL: &mcp.URLElicitationCapabilities{}}}
		return o
	}
	for _, x := range []struct {
		name   string
		proto  string
		client *mcp.ClientOptions
		noConf bool
		reason string
	}{
		{"no elicitation", protoInCall, nil, false, `reason="client does not declare elicitation"`},
		{"no elicitation", protoRoundTrip, nil, false, `reason="client does not declare elicitation form mode"`},
		{"url only", protoRoundTrip, urlOnly(), false, `reason="client does not declare elicitation form mode"`},
		{"RXMCP_CONFIRM=0", protoInCall, (&formClient{answer: &mcp.ElicitResult{Action: "decline"}}).options(), true, `reason="RXMCP_CONFIRM=0"`},
		{"RXMCP_CONFIRM=0", protoRoundTrip, (&formClient{answer: &mcp.ElicitResult{Action: "decline"}}).options(), true, `reason="RXMCP_CONFIRM=0"`},
	} {
		t.Run(x.proto+"/"+x.name, func(t *testing.T) {
			srv, f := newSubstRX(t, 0)
			log := &syncBuf{}
			cs := dial(t, newWriteServer(t, writeServerOpts{RXURL: srv.URL + "/odata", NoConfirm: x.noConf, Log: log}), x.proto, x.client, nil)
			for range 2 {
				res := mustCall(t, cs, "rx_complete_assignment", map[string]any{"id": 22}, nil, "")
				if res.NeedsInput() || res.IsError || resultText(res) != "Задание #22 (простое задание) завершено с результатом Complete." {
					t.Errorf("ответ: %q (форма %v)", resultText(res), res.NeedsInput())
				}
			}
			if n := f.count(completes); n != 2 {
				t.Errorf("выполнений: %d", n)
			}
			out := log.String()
			if n := strings.Count(out, "no write confirmation form"); n != 1 || !strings.Contains(out, x.reason) || !strings.Contains(out, "protocol="+x.proto) {
				t.Errorf("журнал (строк о форме %d):\n%s", n, out)
			}
		})
	}
}

// Новость о версии не уходит в ответ с формой: там не может быть текста, и модель её не
// увидит. Она приходит с ответом на повтор.
func TestUpdateNoticeNotInConfirmForm(t *testing.T) {
	srv, _ := newSubstRX(t, 0)
	s := newWriteServer(t, writeServerOpts{RXURL: srv.URL + "/odata"})
	s.SetUpdate("9.9.9")
	cs := dial(t, s, protoRoundTrip, (&formClient{answer: &mcp.ElicitResult{Action: "accept"}, manual: true}).options(), nil)
	first := mustCall(t, cs, "rx_complete_assignment", map[string]any{"id": 22}, nil, "")
	if askedForm(first) == nil || len(first.Content) != 0 {
		t.Fatalf("форма с текстом: %+v", first)
	}
	res := mustCall(t, cs, "rx_complete_assignment", map[string]any{"id": 22}, acceptAnswer, first.RequestState)
	if out := resultText(res); !strings.HasPrefix(out, "Задание #22 (простое задание) завершено") || !strings.Contains(out, "Вышла rxmcp 9.9.9") {
		t.Errorf("ответ: %q", out)
	}
}

// HTTP с общим секретом (сеансы с Mcp-Session-Id, как в main.go): согласие выполняет задание
// один раз, отказ ничего не меняет, форма та же. Какой путь выберет SDK, решает версия, о
// которой договорились клиент и сервер; поведение для пользователя одинаковое.
func TestConfirmStreamableHTTP(t *testing.T) {
	for _, x := range []struct {
		name   string
		answer *mcp.ElicitResult
		out    string
		writes int
	}{
		{"accept", &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, doneFor21, 1},
		{"decline", &mcp.ElicitResult{Action: "decline"}, declinedText, 0},
	} {
		t.Run(x.name, func(t *testing.T) {
			srv, f := newSubstRX(t, 0)
			s := newWriteServer(t, writeServerOpts{RXURL: srv.URL + "/odata", Confirms: server.NewConfirmations()})
			hs := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.MCP }, nil))
			t.Cleanup(hs.Close)
			fc := &formClient{answer: x.answer}
			cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, fc.options()).Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: hs.URL}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cs.Close() })
			res := mustCall(t, cs, "rx_complete_assignment", map[string]any{"id": 21}, nil, "")
			if res.NeedsInput() || res.IsError || resultText(res) != x.out {
				t.Errorf("протокол %s, ответ: %q (isError %v, форма %v)", cs.InitializeResult().ProtocolVersion, resultText(res), res.IsError, res.NeedsInput())
			}
			if got := fc.shown(); len(got) != 1 || got[0] != formFor21 {
				t.Errorf("формы: %q", got)
			}
			if n := f.count(completes); n != x.writes {
				t.Errorf("в RX ушло выполнений: %d, ждали %d", n, x.writes)
			}
		})
	}
}
