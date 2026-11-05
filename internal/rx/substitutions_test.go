package rx_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/drxinfra/rxmcp/internal/rx"
)

// sub запись ISubstitutions; пустые даты и system == nil уходят как null.
func sub(id, userID int, name, start, end string, system any) map[string]any {
	m := map[string]any{"Id": id, "Name": "Замещение", "Status": "Active", "IsSystem": system, "StartDate": nil, "EndDate": nil,
		"User": map[string]any{"Id": userID, "Name": name}}
	if start != "" {
		m["StartDate"] = start
	}
	if end != "" {
		m["EndDate"] = end
	}
	return m
}

func TestSubstitutionsNone(t *testing.T) {
	f := newFake(t)
	s := newSvc(t, f)
	subs, err := s.Substitutions(context.Background())
	if err != nil || len(subs) != 0 {
		t.Fatalf("без замещений: %v %+v", err, subs)
	}
	if !f.hasQuery("ISubstitutions", "Substitute/Id eq 7 and Status eq 'Active'") {
		t.Error("запрос замещений не ушёл в RX")
	}
	if got := s.F.SubstitutionList(subs); got != "" {
		t.Errorf("пустой список: %q", got)
	}
}

// Действующее, истёкшее и системное замещение: считается только первое.
func TestSubstitutionsOnlyActiveNonSystem(t *testing.T) {
	f := newFake(t)
	s := newSvc(t, f) // сейчас 10.09.2026 15:00 по Москве
	f.setSubs(0,
		sub(1, 12, "Сидорова Анна", "2026-09-01T00:00:00+03:00", "2026-12-31T00:00:00+03:00", false),
		sub(2, 15, "Кузнецов Пётр", "2026-08-01T00:00:00+03:00", "2026-09-01T00:00:00+03:00", false),
		sub(3, 16, "Смирнова Ольга", "", "", true),
	)
	subs, err := s.Substitutions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].User.ID != 12 {
		t.Fatalf("ждали одну Сидорову: %+v", subs)
	}
	if got := s.F.SubstitutionList(subs); got != "Сидорова Анна (#12) по 31.12.2026" {
		t.Errorf("строка: %q", got)
	}
}

func TestSubstitutionsEdges(t *testing.T) {
	f := newFake(t)
	s := newSvc(t, f)
	noUser := sub(9, 0, "", "", "", nil)
	delete(noUser, "User")
	closed := sub(8, 22, "Фёдорова Нина", "", "", false)
	closed["Status"] = "Closed"
	f.setSubs(0,
		sub(1, 12, "Сидорова Анна", "2026-09-01T00:00:00+03:00", "2026-12-31T00:00:00+03:00", false),
		sub(2, 12, "Сидорова Анна", "2026-08-01T00:00:00+03:00", "2026-10-01T00:00:00+03:00", false), // та же, короче
		sub(3, 17, "Орлов Игорь", "", "", nil),                                                       // бессрочно, IsSystem = null
		sub(4, 18, "Белова Вера", "2026-12-01T00:00:00+03:00", "", false),                            // ещё не началось
		sub(5, 19, "Новиков Тимур", "", "2026-09-10T00:00:00+03:00", false),                          // по сегодня включительно
		sub(6, 20, "Егоров Илья", "", "2026-09-09T00:00:00+03:00", false),                            // кончилось вчера
		sub(7, 7, "Иванов Иван", "", "", false),                                                      // сам себя
		closed, noUser,
	)
	subs, err := s.Substitutions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := "Новиков Тимур (#19) по 10.09.2026; Орлов Игорь (#17) бессрочно; Сидорова Анна (#12) по 31.12.2026"
	if got := s.F.SubstitutionList(subs); got != want {
		t.Errorf("замещения:\n%s\nждали:\n%s", got, want)
	}
}

func TestSubstitutionsUnavailable(t *testing.T) {
	for _, code := range []int{400, 403, 404} {
		f := newFake(t)
		s := newSvc(t, f)
		f.setSubs(code)
		for range 2 {
			if _, err := s.Substitutions(context.Background()); !errors.Is(err, rx.ErrSubstitutionsUnavailable) {
				t.Errorf("%d: ждали ErrSubstitutionsUnavailable, получили %v", code, err)
			}
		}
		if n := f.count("/ISubstitutions?"); n != 1 {
			t.Errorf("%d: «недоступно» должно запоминаться, запросов %d", code, n)
		}
	}
	// Сбой сервера это не «не поддерживается», и он не запоминается.
	f := newFake(t)
	s := newSvc(t, f)
	f.setSubs(500)
	for range 2 {
		if _, err := s.Substitutions(context.Background()); err == nil || errors.Is(err, rx.ErrSubstitutionsUnavailable) {
			t.Errorf("500: ждали обычную ошибку, получили %v", err)
		}
	}
	if n := f.count("/ISubstitutions?"); n != 2 {
		t.Errorf("500 не должен запоминаться, запросов %d", n)
	}
}

func TestSubstitutionsCached(t *testing.T) {
	f := newFake(t)
	s := newSvc(t, f)
	f.setSubs(0, sub(1, 12, "Сидорова Анна", "", "", false))
	first, err := s.Substitutions(context.Background())
	if err != nil || len(first) != 1 {
		t.Fatalf("%v %+v", err, first)
	}
	first[0].User.Name = "испорчено"
	second, _ := s.Substitutions(context.Background())
	if n := f.count("/ISubstitutions?"); n != 1 {
		t.Errorf("второй вызов должен взять кэш, запросов %d", n)
	}
	if len(second) != 1 || second[0].User.Name != "Сидорова Анна" {
		t.Errorf("кэш испорчен правкой ответа: %+v", second)
	}
}

func TestMyAssignmentsSubstituted(t *testing.T) {
	f := newFake(t)
	s := newSvc(t, f)
	ctx := context.Background()

	// Без замещаемых запрос и строки прежние.
	items, total, err := s.MyAssignments(ctx, rx.AssignmentFilter{})
	if err != nil {
		t.Fatal(err)
	}
	q, ok := f.query("IAssignments", "Performer/Id eq 7 and Status eq 'InProcess'")
	if !ok || q.Get("$expand") != "Author($select=Id,Name),Task($select=Id,Subject,Status)" {
		t.Errorf("запрос своих заданий изменился: %v", q)
	}
	if txt := s.AssignmentsText(items, total, "заданий"); strings.Contains(txt, " · за ") {
		t.Errorf("пометка «за» в своих заданиях:\n%s", txt)
	}

	// Со своим Id, нулём и повтором в списке: каждый исполнитель в фильтре один раз.
	items, total, err = s.MyAssignments(ctx, rx.AssignmentFilter{Substituted: []int64{12, 7, 0, 12}})
	if err != nil {
		t.Fatal(err)
	}
	if !f.hasQuery("IAssignments", "(Performer/Id eq 7 or Performer/Id eq 12) and Status eq 'InProcess'") {
		t.Error("фильтр с замещаемыми не ушёл в RX")
	}
	txt := s.AssignmentsText(items, total, "заданий")
	if !strings.Contains(txt, "\n#103 ● Подготовить ответ на письмо · срок 11.09 18:00 (завтра) · от Петров Пётр · задача #501 · за Сидорова Анна\n") {
		t.Errorf("нет строки чужого задания с пометкой:\n%s", txt)
	}
	for _, line := range strings.Split(txt, "\n") {
		if strings.HasPrefix(line, "#102 ") && strings.Contains(line, " · за ") {
			t.Errorf("своё задание помечено как чужое: %s", line)
		}
	}
}

// Чужое задание без замещения: отказ RX объясняется, а не выдаётся за неверный результат.
func TestCompleteForeignAssignmentDenied(t *testing.T) {
	f := newFake(t)
	s := newSvc(t, f)
	a, _, err := s.GetAssignment(context.Background(), 556)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CompleteAssignment(context.Background(), a, "")
	if err == nil {
		t.Fatal("ждали отказ RX")
	}
	for _, want := range []string{"RX не дал выполнить задание #556", "исполнитель (Кузнецов Пётр)", "справочнику «Замещения»"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("нет %q в %q", want, err)
		}
	}
	if strings.Contains(err.Error(), "не принял результат") {
		t.Errorf("отказ по правам выдан за неверный результат: %v", err)
	}
}

// Сервер отдаёт страницы меньше $top: справочник дочитывается по @odata.count.
func TestSubstitutionsPaged(t *testing.T) {
	f := newFake(t)
	s := newSvc(t, f)
	f.setSubs(0,
		sub(1, 31, "Ершова Лидия", "", "", true), // системное, на первой странице
		sub(2, 32, "Борисов Глеб", "", "", false),
		sub(3, 33, "Волкова Зоя", "", "", true),
		sub(4, 34, "Гусев Семён", "", "", true),
		sub(5, 35, "Дмитриева Ева", "", "2026-12-31T00:00:00+03:00", false), // на третьей странице
	)
	f.mu.Lock()
	f.subsCap = 2
	f.mu.Unlock()
	subs, err := s.Substitutions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := s.F.SubstitutionList(subs); got != "Борисов Глеб (#32) бессрочно; Дмитриева Ева (#35) по 31.12.2026" {
		t.Errorf("замещения со всех страниц: %q", got)
	}
	if n := f.count("/ISubstitutions?"); n != 3 {
		t.Errorf("ждали три страницы, запросов %d", n)
	}
}
