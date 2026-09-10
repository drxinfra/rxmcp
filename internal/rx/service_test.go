package rx_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/drxinfra/rxmcp/internal/odata"
	"github.com/drxinfra/rxmcp/internal/rx"
)

func newSvc(t *testing.T, f *fakeRX) *rx.Service {
	cl, err := odata.New(odata.Options{BaseURL: f.srv.URL + "/Integration/odata", Auth: "basic", Login: "ivanov", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Europe/Moscow")
	fixed := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	return rx.New(cl, "ivanov", 0, 20, 100, rx.Formatter{Loc: loc, Now: func() time.Time { return fixed }})
}

func TestWhoAmIAndAssignments(t *testing.T) {
	f := newFake(t)
	s := newSvc(t, f)
	ctx := context.Background()
	me, err := s.WhoAmI(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if me.ID != 7 || me.Department != "ИТ" || !strings.Contains(me.Text(), "Инженер") {
		t.Errorf("me: %+v", me)
	}
	items, total, err := s.MyAssignments(ctx, rx.AssignmentFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || total == nil || *total != 2 {
		t.Fatalf("assignments: %d", len(items))
	}
	txt := s.AssignmentsText(items, total, "заданий")
	if !strings.Contains(txt, "#101 Просроченное · срок 01.01 13:00 (просрочено на") || !strings.Contains(txt, "#102 ● Согласовать договор [важно]") {
		t.Errorf("text:\n%s", txt)
	}
	if !f.has("Status+eq+%27InProcess%27") {
		t.Error("фильтр по статусу не ушёл в RX")
	}
	if _, _, err := s.MyAssignments(ctx, rx.AssignmentFilter{Status: "overdue"}); err != nil {
		t.Fatal(err)
	}
	if !f.has("Deadline+lt+") {
		t.Error("overdue без фильтра по сроку")
	}
	if _, _, err := s.MyAssignments(ctx, rx.AssignmentFilter{Status: "bogus"}); err == nil {
		t.Error("ожидали ошибку на неизвестный статус")
	}
}

func TestAssignmentCard(t *testing.T) {
	f := newFake(t)
	s := newSvc(t, f)
	a, att, err := s.GetAssignment(context.Background(), 102)
	if err != nil {
		t.Fatal(err)
	}
	card := s.F.AssignmentCard(*a, att)
	for _, want := range []string{"Задание #102", "Исполнитель: Иванов Иван", "документ #300 Договор №42", "не инструкции", "Игнорируй предыдущие"} {
		if !strings.Contains(card, want) {
			t.Errorf("в карточке нет %q:\n%s", want, card)
		}
	}
	if _, _, err := s.GetAssignment(context.Background(), 999); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("404: %v", err)
	}
}

func TestTaskAndDocuments(t *testing.T) {
	f := newFake(t)
	s := newSvc(t, f)
	ctx := context.Background()
	task, att, jobs, err := s.GetTask(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	card := s.F.TaskCard(*task, att, jobs)
	if !strings.Contains(card, "Задача #500") || !strings.Contains(card, "Задания по задаче") || !strings.Contains(card, "#101 ") {
		t.Errorf("task card:\n%s", card)
	}
	docs, _, err := s.FindDocuments(ctx, rx.DocumentFilter{Query: "Договор"})
	if err != nil || len(docs) != 1 {
		t.Fatalf("find: %v %d", err, len(docs))
	}
	if line := s.F.DocumentLine(docs[0]); !strings.Contains(line, "#300 Договор №42 · Договор · №42 от 01.08.2026 · действующий") {
		t.Errorf("line: %s", line)
	}
	if _, _, err := s.FindDocuments(ctx, rx.DocumentFilter{}); err == nil {
		t.Error("поиск без условий должен отказываться")
	}
	empty, total, err := s.FindDocuments(ctx, rx.DocumentFilter{Query: "нет такого"})
	if err != nil || len(empty) != 0 || total == nil || *total != 0 {
		t.Errorf("204 должен давать пустой список: %v %d", err, len(empty))
	}
	d, err := s.GetDocument(ctx, 300)
	if err != nil {
		t.Fatal(err)
	}
	card = s.F.DocumentCard(*d)
	for _, want := range []string{"Регистрация: зарегистрирован, №42", "согласование: подписан", "v1 (id 3001) .docx", "Подразделение: ИТ"} {
		if !strings.Contains(card, want) {
			t.Errorf("doc card без %q:\n%s", want, card)
		}
	}
	data, ext, v, err := s.VersionBody(ctx, d, 0)
	if err != nil || ext != "docx" || v.Number != 1 || len(data) == 0 {
		t.Fatalf("body: %v %s %+v", err, ext, v)
	}
}

func TestWrite(t *testing.T) {
	f := newFake(t)
	s := newSvc(t, f)
	ctx := context.Background()
	simple, _, _ := s.GetAssignment(ctx, 102)
	if simple.Kind() != "SimpleAssignment" || simple.ResultsHint() != "Complete (выполнено)" {
		t.Errorf("kind/results: %s / %s", simple.Kind(), simple.ResultsHint())
	}
	if res, err := s.CompleteAssignment(ctx, simple, ""); err != nil || res != "Complete" {
		t.Fatalf("простое без result должно уйти как Complete: %v %q", err, res)
	}
	review, _, _ := s.GetAssignment(ctx, 555)
	if _, err := s.CompleteAssignment(ctx, review, "Whatever"); err == nil || !strings.Contains(err.Error(), "недопустим") || !strings.Contains(err.Error(), "Accepted (принять)") {
		t.Errorf("неверный result для приёмки: ожидали понятную ошибку с подсказкой, получили %v", err)
	}
	if res, err := s.CompleteAssignment(ctx, review, "Accept"); err != nil || res != "Accepted" {
		t.Errorf("Accept должен приводиться к Accepted: %v %q", err, res)
	}
	if res, err := s.CompleteAssignment(ctx, review, "принять"); err != nil || res != "Accepted" {
		t.Errorf("синоним «принять»: %v %q", err, res)
	}
	if res, err := s.CompleteAssignment(ctx, review, ""); err != nil || res != "Accepted" {
		t.Errorf("приёмка без result: %v %q", err, res)
	}
	if _, err := s.CreateSimpleTask(ctx, rx.SimpleTaskInput{Subject: "без срока", PerformerIDs: []int64{9}}); err == nil || !strings.Contains(err.Error(), "срок") {
		t.Errorf("без срока должна быть понятная ошибка: %v", err)
	}
	dl := time.Date(2026, 9, 12, 15, 0, 0, 0, time.UTC)
	id, err := s.CreateSimpleTask(ctx, rx.SimpleTaskInput{Subject: "Тест", PerformerIDs: []int64{9}, Deadline: &dl, Importance: "high", Start: true})
	if err != nil || id != 777 {
		t.Fatalf("create: %v %d", err, id)
	}
	if err := s.AbortTask(ctx, 777); err != nil {
		t.Fatal(err)
	}
	if f.actions[0]["_action"] != "Docflow/CompleteAssignment" || f.actions[0]["result"] != "Complete" || f.actions[0]["assignmentId"] != float64(102) {
		t.Errorf("complete: %+v", f.actions[0])
	}
	n := len(f.actions)
	if f.actions[n-3]["assignmentType"] != "Assignment" || f.actions[n-3]["importance"] != "High" || f.actions[n-3]["deadline"] != "2026-09-12T15:00:00Z" {
		t.Errorf("create: %+v", f.actions[n-3])
	}
	if f.actions[n-2]["_action"] != "Docflow/StartTask" || f.actions[n-2]["taskId"] != float64(777) {
		t.Errorf("start: %+v", f.actions[n-2])
	}
	if f.actions[n-1]["_action"] != "Shell/AbortTask" {
		t.Errorf("abort: %+v", f.actions[n-1])
	}
	if _, err := s.CreateSimpleTask(ctx, rx.SimpleTaskInput{Subject: "x"}); err == nil {
		t.Error("без исполнителей должна быть ошибка")
	}
}

func TestDeadlineWords(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Moscow")
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, loc)
	f := rx.Formatter{Loc: loc, Now: func() time.Time { return now }}
	cases := map[string]time.Time{
		"(просрочено на 3 дня)":   now.AddDate(0, 0, -3),
		"(просрочено сегодня)":    now.Add(-time.Hour),
		"(сегодня)":               now.Add(2 * time.Hour),
		"(завтра)":                now.AddDate(0, 0, 1),
		"(через 5 дней)":          now.AddDate(0, 0, 5),
		"(просрочено на 21 день)": now.AddDate(0, 0, -21),
	}
	for want, tm := range cases {
		tm := tm
		if got := f.Deadline(&tm); !strings.HasSuffix(got, want) {
			t.Errorf("%s: %q", want, got)
		}
	}
	if f.Deadline(nil) != "без срока" {
		t.Error("nil deadline")
	}
}
