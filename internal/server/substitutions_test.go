package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/drxinfra/rxmcp/internal/odata"
	"github.com/drxinfra/rxmcp/internal/rx"
	"github.com/drxinfra/rxmcp/internal/server"
)

// substRX фейк RX для замещений: пользователь Иванов (#7), его задание #5 и задание #21
// замещаемой Сидоровой (#12). Справочник «Замещения» отвечает status (если не 0) или subs.
type substRX struct {
	mu   sync.Mutex
	reqs []string // «набор filter=…» по каждому запросу
}

func (f *substRX) log() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.reqs, "\n")
}

func (f *substRX) count(sub string) int {
	return strings.Count(f.log(), sub)
}

func newSubstRX(t *testing.T, status int, subs ...any) (*httptest.Server, *substRX) {
	f := &substRX{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "ivanov" || p != "secret" {
			w.WriteHeader(401)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/odata/")
		filter := r.URL.Query().Get("$filter")
		f.mu.Lock()
		f.reqs = append(f.reqs, p+" filter="+filter)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		own := map[string]any{"Id": 5, "Subject": "Проверить", "Status": "InProcess", "Deadline": "2099-01-01T00:00:00Z", "Author": map[string]any{"Id": 1, "Name": "Автор"}}
		simple := func(id int, subject string, performer map[string]any) map[string]any {
			return map[string]any{"@odata.type": "#X.Workflow.ISimpleAssignmentDto", "Id": id, "Subject": subject, "Status": "InProcess", "Performer": performer}
		}
		switch p {
		case "IUsers":
			json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"Id": 7, "Name": "Иванов Иван", "Login": map[string]any{"LoginName": "ivanov", "TypeAuthentication": "Password"}}}})
		case "ISubstitutions":
			if filter != "Substitute/Id eq 7 and Status eq 'Active'" {
				t.Errorf("фильтр замещений: %s", filter)
			}
			if status != 0 {
				w.WriteHeader(status)
				return
			}
			if len(subs) == 0 {
				w.WriteHeader(204)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"value": subs})
		case "IAssignments", "INotices":
			items := []any{own}
			if strings.Contains(filter, "Performer/Id eq 12") {
				if !strings.Contains(r.URL.Query().Get("$expand"), "Performer($select=Id,Name)") {
					t.Errorf("без исполнителя в $expand: %s", r.URL.Query().Get("$expand"))
				}
				own["Performer"] = map[string]any{"Id": 7, "Name": "Иванов Иван"}
				items = append(items, map[string]any{"Id": 21, "Subject": "Подготовить ответ", "Status": "InProcess", "Deadline": "2099-02-01T00:00:00Z",
					"Author": map[string]any{"Id": 1, "Name": "Автор"}, "Performer": map[string]any{"Id": 12, "Name": "Сидорова Анна"}})
			}
			json.NewEncoder(w).Encode(map[string]any{"@odata.count": len(items), "value": items})
		case "IAssignments(21)":
			json.NewEncoder(w).Encode(simple(21, "Подготовить ответ", map[string]any{"Id": 12, "Name": "Сидорова Анна"}))
		case "IAssignments(22)":
			json.NewEncoder(w).Encode(simple(22, "Своё задание", map[string]any{"Id": 7, "Name": "Иванов Иван"}))
		case "IAssignments(23)":
			json.NewEncoder(w).Encode(simple(23, "Чужое без замещения", map[string]any{"Id": 15, "Name": "Кузнецов Пётр"}))
		case "Docflow/CompleteAssignment":
			var m map[string]any
			json.NewDecoder(r.Body).Decode(&m)
			if m["assignmentId"] == float64(23) {
				w.WriteHeader(400)
				w.Write([]byte(`"Только исполнитель или замещающие пользователи могут выполнить задание."`))
				return
			}
			w.WriteHeader(204)
		default:
			w.WriteHeader(404) // ITasks, IEmployees(7)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

// substSession клиент MCP к серверу на фейке.
func substSession(t *testing.T, rxURL string) *mcp.ClientSession {
	t.Helper()
	cl, err := odata.New(odata.Options{BaseURL: rxURL + "/odata", Auth: "basic", Login: "ivanov", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	s := server.New(rx.New(cl, "ivanov", 0, 20, 100, rx.Formatter{Loc: time.UTC}), server.Options{Version: "test", MaxTextChars: 20000})
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := s.MCP.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func substCall(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func subst(id, userID int, name, start, end string, system any) map[string]any {
	m := map[string]any{"Id": id, "Status": "Active", "IsSystem": system, "StartDate": nil, "EndDate": nil, "User": map[string]any{"Id": userID, "Name": name}}
	if start != "" {
		m["StartDate"] = start
	}
	if end != "" {
		m["EndDate"] = end
	}
	return m
}

// Действующее, бессрочное, истёкшее и системное замещения.
var substAll = []any{
	subst(1, 12, "Сидорова Анна", "2026-01-01T00:00:00+03:00", "2099-12-31T00:00:00+03:00", false),
	subst(2, 15, "Кузнецов Пётр", "2020-01-01T00:00:00+03:00", "2020-02-01T00:00:00+03:00", false),
	subst(3, 16, "Смирнова Ольга", "", "", true),
	subst(4, 17, "Орлов Игорь", "", "", nil),
}

func TestWhoAmIShowsSubstitutions(t *testing.T) {
	srv, _ := newSubstRX(t, 0, substAll...)
	out, isErr := substCall(t, substSession(t, srv.URL), "rx_whoami", map[string]any{})
	want := "Вы в RX: Иванов Иван (id 7)\nЛогин: ivanov (тип входа Password)\n" +
		"Замещаете: Орлов Игорь (#17) бессрочно; Сидорова Анна (#12) по 31.12.2099. Их задания покажут rx_my_assignments и rx_whats_new с include_substituted=true.\n" +
		"rxmcp test"
	if isErr || out != want {
		t.Errorf("whoami:\n%s\nждали:\n%s", out, want)
	}
	// Замещений нет, справочник недоступен или RX сбоит: строки нет, whoami работает.
	for _, status := range []int{0, 404, 403, 500} {
		srv, _ := newSubstRX(t, status)
		out, isErr := substCall(t, substSession(t, srv.URL), "rx_whoami", map[string]any{})
		if isErr || out != "Вы в RX: Иванов Иван (id 7)\nЛогин: ivanov (тип входа Password)\nrxmcp test" {
			t.Errorf("%d: %q", status, out)
		}
	}
}

func TestMyAssignmentsIncludeSubstituted(t *testing.T) {
	srv, f := newSubstRX(t, 0, substAll...)
	cs := substSession(t, srv.URL)
	plain, _ := substCall(t, cs, "rx_my_assignments", map[string]any{})
	n := f.count("ISubstitutions")
	off, isErr := substCall(t, cs, "rx_my_assignments", map[string]any{"include_substituted": false})
	if isErr || off != plain || f.count("ISubstitutions") != n {
		t.Errorf("include_substituted=false должен давать прежний ответ без запроса замещений:\n%s\n---\n%s", off, plain)
	}
	if strings.Contains(plain, "за ") || strings.Contains(plain, "амещ") {
		t.Errorf("без флага ответ изменился:\n%s", plain)
	}

	out, isErr := substCall(t, cs, "rx_my_assignments", map[string]any{"include_substituted": true})
	if isErr {
		t.Fatal(out)
	}
	if !strings.HasPrefix(out, "Добавлены задания тех, кого вы замещаете: Орлов Игорь (#17), Сидорова Анна (#12); их строки помечены «· за <ФИО>».\nЗаданий: 2\n") {
		t.Errorf("нет строки о замещаемых:\n%s", out)
	}
	lines := strings.Split(out, "\n")
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "#21 "):
			if !strings.HasSuffix(l, " · от Автор · за Сидорова Анна") {
				t.Errorf("чужая строка без пометки: %s", l)
			}
		case strings.HasPrefix(l, "#5 "):
			if strings.Contains(l, " · за ") {
				t.Errorf("своя строка с пометкой: %s", l)
			}
		}
	}
	if !strings.Contains(f.log(), "IAssignments filter=(Performer/Id eq 7 or Performer/Id eq 17 or Performer/Id eq 12) and Status eq 'InProcess'") {
		t.Errorf("фильтр с замещаемыми не ушёл:\n%s", f.log())
	}
}

func TestMyAssignmentsSubstitutedNoneOrUnavailable(t *testing.T) {
	for status, note := range map[int]string{
		0:   "Действующих замещений у вас нет: показаны только ваши задания.\n",
		404: "Замещения в этом RX не читаются: справочника «Замещения» нет в сервисе интеграции или на него нет прав. Показаны только ваши задания.\n",
		403: "Замещения в этом RX не читаются: справочника «Замещения» нет в сервисе интеграции или на него нет прав. Показаны только ваши задания.\n",
	} {
		srv, f := newSubstRX(t, status, subst(3, 16, "Смирнова Ольга", "", "", true)) // только системное
		cs := substSession(t, srv.URL)
		plain, _ := substCall(t, cs, "rx_my_assignments", map[string]any{})
		out, isErr := substCall(t, cs, "rx_my_assignments", map[string]any{"include_substituted": true})
		if isErr || out != note+plain {
			t.Errorf("%d:\n%s\nждали:\n%s", status, out, note+plain)
		}
		if strings.Contains(f.log(), " or ") {
			t.Errorf("%d: в фильтре чужие исполнители:\n%s", status, f.log())
		}
	}
	// Уведомления: та же строка, но про уведомления.
	srv, _ := newSubstRX(t, 0)
	out, _ := substCall(t, substSession(t, srv.URL), "rx_my_assignments", map[string]any{"include_substituted": true, "notices": true})
	if !strings.HasPrefix(out, "Действующих замещений у вас нет: показаны только ваши уведомления.\nУведомлений: 1\n") {
		t.Errorf("уведомления:\n%s", out)
	}
}

func TestWhatsNewIncludeSubstituted(t *testing.T) {
	srv, f := newSubstRX(t, 0, substAll...)
	cs := substSession(t, srv.URL)
	out, isErr := substCall(t, cs, "rx_whats_new", map[string]any{"since": "2026-10-15", "peek": true, "include_substituted": true})
	if isErr {
		t.Fatal(out)
	}
	if !strings.HasPrefix(out, "Что нового в RX с 15.10.2026 00:00 (по запросу).\nДобавлены задания и уведомления тех, кого вы замещаете: Орлов Игорь (#17), Сидорова Анна (#12); их строки помечены «· за <ФИО>».\n\nНовых заданий: 2\n") {
		t.Errorf("начало ответа:\n%s", out)
	}
	if strings.Count(out, " · за Сидорова Анна") != 3 { // новые задания, уведомления, срок сегодня
		t.Errorf("пометки в разделах:\n%s", out)
	}
	or := "(Performer/Id eq 7 or Performer/Id eq 17 or Performer/Id eq 12)"
	for _, want := range []string{"IAssignments filter=" + or + " and Created ge ", "INotices filter=" + or + " and Created ge ", "IAssignments filter=" + or + " and Status eq 'InProcess' and Deadline lt "} {
		if !strings.Contains(f.log(), want) {
			t.Errorf("нет запроса %q:\n%s", want, f.log())
		}
	}

	// Справочник недоступен: сказано один раз, остальное как без флага.
	srv, _ = newSubstRX(t, 404)
	cs = substSession(t, srv.URL)
	args := map[string]any{"since": "2026-10-15", "peek": true}
	plain, _ := substCall(t, cs, "rx_whats_new", args)
	args["include_substituted"] = true
	out, isErr = substCall(t, cs, "rx_whats_new", args)
	note := "Замещения в этом RX не читаются: справочника «Замещения» нет в сервисе интеграции или на него нет прав. Показаны только ваши задания и уведомления.\n"
	head, rest, _ := strings.Cut(plain, "\n")
	if isErr || out != head+"\n"+note+rest {
		t.Errorf("недоступно:\n%s\nбез флага:\n%s", out, plain)
	}
	if strings.Count(out, "Замещения в этом RX не читаются") != 1 {
		t.Errorf("про недоступность больше одного раза:\n%s", out)
	}
}

func TestCompleteSubstitutedAssignment(t *testing.T) {
	srv, _ := newSubstRX(t, 0)
	call := func(args map[string]any) rawCallResult {
		return rawToolCall(t, rawCallOpts{RXURL: srv.URL + "/odata", Accept: true}, "rx_complete_assignment", args)
	}
	r := call(map[string]any{"id": 21})
	if r.Asked != "Завершить простое задание #21 «Подготовить ответ» за Сидорова Анна? Исполнитель задания не вы, а Сидорова Анна: RX разрешает выполнять чужие задания только замещающему." {
		t.Errorf("подтверждение: %q", r.Asked)
	}
	if r.IsError || r.Text != "Задание #21 (простое задание) выполнено за Сидорова Анна с результатом Complete." {
		t.Errorf("ответ: %s", r.Text)
	}
	r = call(map[string]any{"id": 21, "result": "выполнено"})
	if r.Asked != "Завершить простое задание #21 «Подготовить ответ» за Сидорова Анна с результатом выполнено? Исполнитель задания не вы, а Сидорова Анна: RX разрешает выполнять чужие задания только замещающему." {
		t.Errorf("подтверждение с результатом: %q", r.Asked)
	}

	// Своё задание: как раньше.
	r = call(map[string]any{"id": 22})
	if r.Asked != "Завершить простое задание #22 «Своё задание»?" || r.IsError || r.Text != "Задание #22 (простое задание) завершено с результатом Complete." {
		t.Errorf("своё задание: %q / %s", r.Asked, r.Text)
	}

	// Чужое без замещения: RX отказывает, ответ объясняет почему.
	r = call(map[string]any{"id": 23})
	if !r.IsError || !strings.Contains(r.Text, "RX не дал выполнить задание #23: выполнить его может только исполнитель (Кузнецов Пётр) или тот, кто его замещает") {
		t.Errorf("отказ RX: %s", r.Text)
	}
}
