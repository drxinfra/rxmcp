package rx_test

import (
	"context"
	"strings"
	"testing"

	"github.com/drxinfra/rxmcp/internal/rx"
)

func TestKnowledgeBase(t *testing.T) {
	f := newFake(t)
	s := newSvc(t, f)
	ctx := context.Background()
	areas, err := s.KBAreas(ctx, "")
	if err != nil || len(areas) != 1 {
		t.Fatalf("areas: %v %d", err, len(areas))
	}
	if txt := rx.AreasText(areas); !strings.Contains(txt, "#115 Общая область · стартовая статья #1 Старт · общая") {
		t.Errorf("areas text: %s", txt)
	}
	items, _, err := s.KBFindArticles(ctx, rx.ArticleFilter{AreaID: 115})
	if err != nil || len(items) != 1 {
		t.Fatalf("find: %v %d", err, len(items))
	}
	if line := s.F.ArticleLine(items[0]); !strings.Contains(line, "#2 Оформление статьи · Общая область · Автор · 06.05.2026") {
		t.Errorf("line: %s", line)
	}
	if !f.has("LifeCycleState+eq+%27Active%27") {
		t.Error("без include_drafts должен быть фильтр по состоянию")
	}
	a, body, err := s.KBArticle(ctx, 2)
	if err != nil || !strings.HasPrefix(body, "# Заголовок") {
		t.Fatalf("article: %v %q", err, body)
	}
	card := s.F.ArticleCard(*a, body, false, 0, 0)
	for _, want := range []string{"Статья #2", "Теги: howto", "Области: Общая область (#115)", "Текст статьи (markdown"} {
		if !strings.Contains(card, want) {
			t.Errorf("card без %q:\n%s", want, card)
		}
	}
	if _, _, err := s.KBFindArticles(ctx, rx.ArticleFilter{}); err == nil {
		t.Error("поиск без условий должен отказывать")
	}
}

func TestBoards(t *testing.T) {
	f := newFake(t)
	s := newSvc(t, f)
	ctx := context.Background()
	boards, err := s.Boards(ctx, "", false, 0)
	if err != nil || len(boards) != 1 {
		t.Fatalf("boards: %v", err)
	}
	if line := rx.BoardLine(boards[0]); line != "#2 Доска команды [DK] · владелец Владелец" {
		t.Errorf("board line: %s", line)
	}
	bd, cols, err := s.BoardByID(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 2 || cols[0].Name != "Новые" || cols[1].Name != "В работе" {
		t.Fatalf("порядок колонок по IndexColumn: %+v", cols)
	}
	if len(cols[0].Tickets) != 1 {
		t.Errorf("дубли карточек должны схлопываться: %d", len(cols[0].Tickets))
	}
	card := s.F.BoardCard(*bd, cols)
	if !strings.Contains(card, "## Новые (1)") || !strings.Contains(card, "DK-3 (id 245) Задача 2 · Иванов Иван · срок") {
		t.Errorf("board card:\n%s", card)
	}
	tk, err := s.TicketByID(ctx, 245)
	if err != nil {
		t.Fatal(err)
	}
	tc := s.F.TicketCard(*tk)
	for _, want := range []string{"Карточка DK-3 (id 245)", "приоритет 8/10", "Исполнители: Иванов Иван", "Трудоёмкость: план 4.5", "Теги: bug", "Задача З-20 (id 20)", "Сделать хорошо"} {
		if !strings.Contains(tc, want) {
			t.Errorf("ticket card без %q:\n%s", want, tc)
		}
	}
	found, _, err := s.FindTickets(ctx, "dk-3", 0, "", 0)
	if err != nil || len(found) != 1 {
		t.Fatalf("find tickets: %v", err)
	}
	if !f.has("Uid+eq+%27DK-3%27") {
		t.Error("код карточки должен искаться по Uid в верхнем регистре")
	}
}

func TestProjects(t *testing.T) {
	f := newFake(t)
	s := newSvc(t, f)
	ctx := context.Background()
	items, _, err := s.Projects(ctx, rx.ProjectFilter{})
	if err != nil || len(items) != 1 {
		t.Fatalf("projects: %v", err)
	}
	if line := s.F.ProjectLine(items[0]); !strings.Contains(line, "#2 Проект (П) · исполнение · состояние: есть риски · рук. Руководитель · 06.05.2026–— · 40%") {
		t.Errorf("project line: %s", line)
	}
	p, plans, err := s.ProjectByID(ctx, 2)
	if err != nil || len(plans) != 1 {
		t.Fatalf("project: %v %d", err, len(plans))
	}
	card := s.F.ProjectCard(*p, plans)
	for _, want := range []string{"Проект #2: Проект", "руководство: Руководитель", "участники: Иванов Иван", "G1 · план 01.06.2026 · пройден 02.06.2026", "#55 План проекта · черновик"} {
		if !strings.Contains(card, want) {
			t.Errorf("project card без %q:\n%s", want, card)
		}
	}
	pl, m, names, err := s.PlanByID(ctx, 55)
	if err != nil || m == nil || len(m.Activities) != 3 || names[7] != "Иванов Иван" {
		t.Fatalf("plan: %v %+v %v", err, m, names)
	}
	pc := s.F.PlanCard(*pl, m, names, 150)
	for _, want := range []string{"▸ Раздел · 24.09.26–28.09.26", "  • Работа · 01.01.20–05.01.20 · Иванов Иван · 50% · ПРОСРОЧЕНО", "  ◆ Веха · —–01.10.26 · 100%", "Итого: выполнено 1, просрочено 1 из 3."} {
		if !strings.Contains(pc, want) {
			t.Errorf("plan card без %q:\n%s", want, pc)
		}
	}
	if pc2 := s.F.PlanCard(*pl, m, names, 1); !strings.Contains(pc2, "показано 1 работ из 3") {
		t.Errorf("max_rows: %s", pc2)
	}
}

// agileActions оставляет из записанных действий только действия досок.
func agileActions(f *fakeRX) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, a := range f.actions {
		if strings.HasPrefix(a["_action"].(string), "AgileBoards/") {
			out = append(out, a)
		}
	}
	return out
}

func TestCreateColumn(t *testing.T) {
	f := newFake(t)
	s := newSvc(t, f)
	ctx := context.Background()
	// Без места обычная колонка встаёт перед финальной «Готово»; закрытая «Старая» не считается.
	b, col, pos, notes, err := s.CreateColumn(ctx, rx.ColumnInput{Board: "2", Name: " На проверке ", WipLimit: 5})
	if err != nil || b.ID != 2 || col.ID != 21 || col.Name != "На проверке" || pos != 3 || len(notes) != 0 {
		t.Fatalf("create: %v %+v pos=%d notes=%v", err, col, pos, notes)
	}
	acts := agileActions(f)
	if len(acts) != 3 {
		t.Fatalf("ждали CreateColumn, UpdateColumnConfig, MoveColumn: %+v", acts)
	}
	if acts[0]["_action"] != "AgileBoards/CreateColumn" || acts[0]["boardId"] != float64(2) {
		t.Errorf("create: %+v", acts[0])
	}
	if u := acts[1]; u["_action"] != "AgileBoards/UpdateColumnConfig" || u["columnId"] != float64(21) || u["name"] != "На проверке" || u["isFinal"] != false || u["wipLimit"] != float64(5) {
		t.Errorf("config: %+v", u)
	}
	if m := acts[2]; m["_action"] != "AgileBoards/MoveColumn" || m["columnRefId"] != float64(31) || m["position"] != float64(2) {
		t.Errorf("move (позиции RX с нуля): %+v", m)
	}
	// Явное место считается с единицы.
	if _, _, pos, _, err := s.CreateColumn(ctx, rx.ColumnInput{Board: "2", Name: "Бэклог", Position: 1}); err != nil || pos != 1 {
		t.Fatalf("position 1: %v %d", err, pos)
	}
	if m := agileActions(f)[5]; m["position"] != float64(0) {
		t.Errorf("move на первое место: %+v", m)
	}
	// Финальная без места остаётся в конце: MoveColumn не нужен.
	n := len(agileActions(f))
	if _, _, pos, _, err := s.CreateColumn(ctx, rx.ColumnInput{Board: "2", Name: "Архив", IsFinal: true}); err != nil || pos != 4 {
		t.Fatalf("final: %v %d", err, pos)
	}
	if got := len(agileActions(f)) - n; got != 2 {
		t.Errorf("для финальной в конце ждали 2 действия, было %d", got)
	}
	// Дубль имени отклоняется до любых действий.
	n = len(agileActions(f))
	if _, _, _, _, err := s.CreateColumn(ctx, rx.ColumnInput{Board: "2", Name: "в работе"}); err == nil || !strings.Contains(err.Error(), "уже есть") {
		t.Errorf("дубль: %v", err)
	}
	if _, _, _, _, err := s.CreateColumn(ctx, rx.ColumnInput{Board: "2", Name: "  "}); err == nil {
		t.Error("пустое имя должно отклоняться")
	}
	if len(agileActions(f)) != n {
		t.Error("при ошибке проверки на доску ничего не должно уходить")
	}
}

func TestRemoveTickets(t *testing.T) {
	f := newFake(t)
	s := newSvc(t, f)
	ctx := context.Background()
	ts, err := s.TicketsByIDs(ctx, []int64{245, 245, 0})
	if err != nil || len(ts) != 1 || ts[0].BoardID != 2 {
		t.Fatalf("tickets: %v %+v", err, ts)
	}
	if _, err := s.TicketsByIDs(ctx, nil); err == nil {
		t.Error("пустой список должен отклоняться")
	}
	// 250 висит на доске двумя ссылками, одна заблокирована; 777 на доске нет.
	ts = append(ts, rx.Ticket{ID: 250, UID: "DK-5", BoardID: 2}, rx.Ticket{ID: 777, BoardID: 2})
	res, err := s.RemoveTickets(ctx, ts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Removed) != 1 || res.Removed[0].ID != 245 || len(res.Blocked) != 1 || res.Blocked[0].ID != 250 || len(res.NotOnBoard) != 1 || res.NotOnBoard[0].ID != 777 {
		t.Fatalf("итог: %+v", res)
	}
	acts := agileActions(f)
	if len(acts) != 1 || acts[0]["_action"] != "AgileBoards/RemoveTickets" || acts[0]["boardId"] != float64(2) {
		t.Fatalf("ждали одно RemoveTickets на доску: %+v", acts)
	}
	refs := acts[0]["ticketRefIds"].([]any)
	if len(refs) != 3 || refs[0] != float64(901) {
		t.Errorf("снимаются ссылки, а не Id карточек, и все ссылки дубля: %v", refs)
	}
}
