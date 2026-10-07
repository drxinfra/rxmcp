package server

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/drxinfra/rxmcp/internal/rx"
	"github.com/drxinfra/rxmcp/internal/textx"
)

type kbAreasIn struct {
	Query string `json:"query,omitempty" jsonschema:"подстрока названия области"`
}

type kbSearchIn struct {
	Query  string `json:"query,omitempty" jsonschema:"подстрока названия статьи"`
	AreaID int64  `json:"area_id,omitempty" jsonschema:"Id области знаний (rx_kb_areas)"`
	Tag    string `json:"tag,omitempty" jsonschema:"подстрока тега"`
	Author string `json:"author,omitempty" jsonschema:"подстрока имени автора"`
	All    bool   `json:"include_drafts,omitempty" jsonschema:"true = включая черновики и устаревшие"`
	Limit  int    `json:"limit,omitempty" jsonschema:"сколько строк показать, по умолчанию 20, максимум 100"`
}

type kbArticleIn struct {
	ID       int64 `json:"id" jsonschema:"Id статьи"`
	MaxChars int   `json:"max_chars,omitempty" jsonschema:"лимит символов текста, по умолчанию из настроек сервера (20000)"`
	Offset   int   `json:"offset,omitempty" jsonschema:"с какого символа продолжить, если статья была обрезана"`
}

type boardsIn struct {
	Query string `json:"query,omitempty" jsonschema:"подстрока названия или префикс доски"`
	All   bool   `json:"include_closed,omitempty" jsonschema:"true = включая закрытые доски; по умолчанию только открытые"`
	Limit int    `json:"limit,omitempty" jsonschema:"сколько строк показать, по умолчанию 20, максимум 100"`
}

type ticketsIn struct {
	Query   string `json:"query,omitempty" jsonschema:"подстрока названия или код карточки вида ABC-12"`
	BoardID int64  `json:"board_id,omitempty" jsonschema:"Id доски из rx_boards, чтобы искать только на ней; без него поиск по всем доскам"`
	Status  string `json:"status,omitempty" jsonschema:"active (по умолчанию), closed, all"`
	Limit   int    `json:"limit,omitempty" jsonschema:"сколько строк показать, по умолчанию 20, максимум 100"`
}

type projectsIn struct {
	Query   string `json:"query,omitempty" jsonschema:"подстрока названия или краткого имени"`
	Manager string `json:"manager,omitempty" jsonschema:"подстрока имени руководителя"`
	Stage   string `json:"stage,omitempty" jsonschema:"Initiation, Planning, Execution, Closing"`
	Mine    bool   `json:"mine,omitempty" jsonschema:"true = только где я руководитель, администратор или в команде"`
	All     bool   `json:"include_closed,omitempty" jsonschema:"true = включая закрытые проекты; по умолчанию только открытые"`
	Limit   int    `json:"limit,omitempty" jsonschema:"сколько строк показать, по умолчанию 20, максимум 100"`
}

type plansIn struct {
	Query     string `json:"query,omitempty" jsonschema:"подстрока названия плана"`
	ProjectID int64  `json:"project_id,omitempty" jsonschema:"Id проекта из rx_projects, чтобы показать только его планы"`
	Limit     int    `json:"limit,omitempty" jsonschema:"сколько строк показать, по умолчанию 20, максимум 100"`
}

type planIn struct {
	ID      int64 `json:"id" jsonschema:"Id плана проекта (документа)"`
	MaxRows int   `json:"max_rows,omitempty" jsonschema:"сколько работ показать, по умолчанию 150"`
}

func (s *Server) registerModules() {
	// База знаний
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_kb_areas",
		Description: "Области базы знаний компании в Directum RX (модуль «Знания»): Id, название, стартовая статья. Используйте, чтобы узнать, какие разделы знаний есть, и сузить rx_kb_search по area_id. Это внутренние статьи компании; вопросы о работе самой системы задавайте rx_help_search. Только чтение.",
		Annotations: ro("Области знаний"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in kbAreasIn) (*mcp.CallToolResult, any, error) {
		items, err := s.svc.KBAreas(ctx, in.Query)
		if err != nil {
			return fail(err)
		}
		return text(rx.AreasText(items)), nil, nil
	})
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_kb_search",
		Description: "Поиск статей базы знаний компании по названию, области, тегу или автору. По тексту статей не ищет. Используйте для вопросов о правилах и инструкциях компании: «как оформить командировку», «регламент закупок». Строка: #Id название · области · автор · дата изменения. Текст статьи отдаёт rx_kb_article. Как устроена сама система, ищите в rx_help_search.",
		Annotations: ro("Поиск статей"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in kbSearchIn) (*mcp.CallToolResult, any, error) {
		items, total, err := s.svc.KBFindArticles(ctx, rx.ArticleFilter{Query: in.Query, AreaID: in.AreaID, Tag: in.Tag, Author: in.Author, AllStat: in.All, Limit: in.Limit})
		if err != nil {
			return fail(err)
		}
		if len(items) == 0 {
			return text("Статей не найдено."), nil, nil
		}
		var b strings.Builder
		if total != nil && int(*total) > len(items) {
			fmt.Fprintf(&b, "Статьи: показано %d из %d\n", len(items), *total)
		} else {
			fmt.Fprintf(&b, "Статьи: %d\n", len(items))
		}
		for _, a := range items {
			b.WriteString(s.svc.F.ArticleLine(a))
			b.WriteString("\n")
		}
		b.WriteString("Текст: rx_kb_article.")
		return text(b.String()), nil, nil
	})
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_kb_article",
		Description: "Статья базы знаний компании целиком: области, теги, автор и текст в markdown. Id берётся из rx_kb_search или из стартовой статьи области в rx_kb_areas. Длинный текст обрезается по max_chars, продолжение запрашивается через offset. Только чтение.",
		Annotations: ro("Статья"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in kbArticleIn) (*mcp.CallToolResult, any, error) {
		a, body, err := s.svc.KBArticle(ctx, in.ID)
		if err != nil {
			return fail(err)
		}
		limit := in.MaxChars
		if limit <= 0 || limit > s.opt.MaxTextChars*5 {
			limit = s.opt.MaxTextChars
		}
		runes := []rune(body)
		if in.Offset > 0 && in.Offset < len(runes) {
			runes = runes[in.Offset:]
		}
		out, cut := textx.Cut(string(runes), limit)
		return text(s.svc.F.ArticleCard(*a, out, cut, in.Offset, limit)), nil, nil
	})

	// Agile-доски
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_boards",
		Description: "Список agile-досок Directum RX: Id, название, префикс карточек, владелец, проект. Используйте, чтобы найти доску и её Id перед rx_board, rx_tickets или rx_create_ticket. По умолчанию только открытые доски, до 20 строк; include_closed=true добавляет закрытые. Содержимое доски показывает rx_board. Только чтение.",
		Annotations: ro("Доски"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in boardsIn) (*mcp.CallToolResult, any, error) {
		items, err := s.svc.Boards(ctx, in.Query, in.All, in.Limit)
		if err != nil {
			return fail(err)
		}
		if len(items) == 0 {
			return text("Досок не найдено."), nil, nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Доски: %d\n", len(items))
		for _, bd := range items {
			b.WriteString(rx.BoardLine(bd))
			b.WriteString("\n")
		}
		return text(strings.TrimRight(b.String(), "\n")), nil, nil
	})
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_board",
		Description: "Одна agile-доска целиком: колонки слева направо и карточки в них с кодом, исполнителями и сроками. Используйте для вопроса «что сейчас в работе на доске». Id доски берётся из rx_boards. Если доска неизвестна или нужна карточка по названию, используйте rx_tickets. Только чтение.",
		Annotations: ro("Доска"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in boardIDIn) (*mcp.CallToolResult, any, error) {
		bd, cols, err := s.svc.BoardByID(ctx, in.ID)
		if err != nil {
			return fail(err)
		}
		return text(s.svc.F.BoardCard(*bd, cols)), nil, nil
	})
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_tickets",
		Description: "Поиск карточек на agile-досках по названию или коду вида ABC-12, с фильтром по доске и статусу. Используйте, когда доска неизвестна или нужна конкретная карточка. Строка: код (id N) название · исполнители · срок, где N это числовой Id для rx_ticket и rx_update_ticket. По умолчанию только активные карточки. Карточку целиком показывает rx_ticket, всю доску rx_board.",
		Annotations: ro("Поиск карточек"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ticketsIn) (*mcp.CallToolResult, any, error) {
		items, total, err := s.svc.FindTickets(ctx, in.Query, in.BoardID, in.Status, in.Limit)
		if err != nil {
			return fail(err)
		}
		if len(items) == 0 {
			return text("Карточек не найдено."), nil, nil
		}
		var b strings.Builder
		if total != nil && int(*total) > len(items) {
			fmt.Fprintf(&b, "Карточки: показано %d из %d\n", len(items), *total)
		} else {
			fmt.Fprintf(&b, "Карточки: %d\n", len(items))
		}
		for _, t := range items {
			fmt.Fprintf(&b, "%s · доска #%d\n", s.svc.F.TicketLine(t), t.BoardID)
		}
		return text(strings.TrimRight(b.String(), "\n")), nil, nil
	})
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_ticket",
		Description: "Одна карточка agile-доски целиком: статус, колонка, исполнители, сроки, трудоёмкость, теги, вложения, описание и комментарии с авторами и временем. Нужен числовой Id карточки: значение id в скобках из rx_tickets или rx_board. Код вида ABC-12 сюда не подходит, по коду ищет rx_tickets. Только чтение, изменить карточку можно через rx_update_ticket, добавить комментарий через rx_comment_ticket.",
		Annotations: ro("Карточка"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ticketIDIn) (*mcp.CallToolResult, any, error) {
		t, err := s.svc.TicketByID(ctx, in.ID)
		if err != nil {
			return fail(err)
		}
		return text(s.svc.F.TicketCard(*t) + s.ticketComments(ctx, t)), nil, nil
	})

	// Проекты и планы
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_projects",
		Description: "Проекты и инициативы Directum RX: стадия, состояние, руководитель, сроки, процент выполнения. Используйте для обзора «какие проекты идут» и чтобы найти Id проекта. mine=true оставит проекты, где пользователь руководитель, администратор или в команде. По умолчанию только открытые, до 20 строк. Карточку проекта показывает rx_project.",
		Annotations: ro("Проекты"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in projectsIn) (*mcp.CallToolResult, any, error) {
		items, total, err := s.svc.Projects(ctx, rx.ProjectFilter{Query: in.Query, Manager: in.Manager, Stage: in.Stage, Member: in.Mine, All: in.All, Limit: in.Limit})
		if err != nil {
			return fail(err)
		}
		if len(items) == 0 {
			return text("Проектов не найдено."), nil, nil
		}
		var b strings.Builder
		if total != nil && int(*total) > len(items) {
			fmt.Fprintf(&b, "Проекты: показано %d из %d\n", len(items), *total)
		} else {
			fmt.Fprintf(&b, "Проекты: %d\n", len(items))
		}
		for _, p := range items {
			b.WriteString(s.svc.F.ProjectLine(p))
			b.WriteString("\n")
		}
		return text(strings.TrimRight(b.String(), "\n")), nil, nil
	})
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_project",
		Description: "Карточка проекта: стадия, плановые и фактические сроки, руководитель, заказчик, команда по группам, гейты, описание и список планов с их Id. Используйте для вопроса «в каком состоянии проект и кто в нём участвует». Id проекта берётся из rx_projects. Дерево работ показывает rx_project_plan. Только чтение.",
		Annotations: ro("Проект"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in projectIDIn) (*mcp.CallToolResult, any, error) {
		p, plans, err := s.svc.ProjectByID(ctx, in.ID)
		if err != nil {
			return fail(err)
		}
		return text(s.svc.F.ProjectCard(*p, plans)), nil, nil
	})
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_project_plans",
		Description: "Поиск планов проектов по названию или по Id проекта. Строка: #Id название · состояние · проект · сроки · процент. Используйте, чтобы найти Id плана перед rx_project_plan, когда проект неизвестен; планы одного проекта уже перечислены в rx_project. Только чтение.",
		Annotations: ro("Планы проектов"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in plansIn) (*mcp.CallToolResult, any, error) {
		items, total, err := s.svc.FindPlans(ctx, in.Query, in.ProjectID, in.Limit)
		if err != nil {
			return fail(err)
		}
		if len(items) == 0 {
			return text("Планов не найдено."), nil, nil
		}
		var b strings.Builder
		if total != nil && int(*total) > len(items) {
			fmt.Fprintf(&b, "Планы: показано %d из %d\n", len(items), *total)
		} else {
			fmt.Fprintf(&b, "Планы: %d\n", len(items))
		}
		for _, p := range items {
			b.WriteString(s.svc.F.PlanLine(p))
			b.WriteString("\n")
		}
		return text(strings.TrimRight(b.String(), "\n")), nil, nil
	})
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_project_plan",
		Description: "План проекта с деревом работ: разделы, работы, вехи, сроки, ответственные, проценты и просрочки. Используйте для вопросов «как идёт проект», «что отстаёт», «кто за что отвечает». Нужен Id плана (документа) из rx_project или rx_project_plans, а не Id проекта. Большой план обрезается по max_rows. Только чтение.",
		Annotations: ro("План проекта"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in planIn) (*mcp.CallToolResult, any, error) {
		p, m, names, err := s.svc.PlanByID(ctx, in.ID)
		if err != nil {
			return fail(err)
		}
		rows := in.MaxRows
		if rows <= 0 {
			rows = 150
		}
		return text(s.svc.F.PlanCard(*p, m, names, rows)), nil, nil
	})
}

// --- запись в agile-доски (только при RXMCP_ALLOW_WRITE=1) ---

type attachIn struct {
	URL        string `json:"url,omitempty" jsonschema:"ссылка http или https"`
	File       string `json:"file,omitempty" jsonschema:"полный путь к файлу на компьютере, где запущен rxmcp; файл загрузится в хранилище доски, до 20 МБ"`
	DocumentID int64  `json:"document_id,omitempty" jsonschema:"Id документа RX из rx_find_documents: к карточке добавится ссылка на него"`
	Name       string `json:"name,omitempty" jsonschema:"подпись вложения; по умолчанию имя файла, название документа или сама ссылка"`
}

func attachInputs(in []attachIn) ([]rx.AttachmentInput, []string) {
	out := make([]rx.AttachmentInput, 0, len(in))
	labels := make([]string, 0, len(in))
	for _, a := range in {
		x := rx.AttachmentInput{URL: a.URL, File: a.File, DocumentID: a.DocumentID, Name: a.Name}
		out = append(out, x)
		labels = append(labels, x.Label())
	}
	return out, labels
}

type createTicketIn struct {
	Board       string     `json:"board" jsonschema:"доска: название, префикс или Id (rx_boards)"`
	Column      string     `json:"column,omitempty" jsonschema:"колонка: название или Id; по умолчанию первая колонка доски"`
	Name        string     `json:"name" jsonschema:"название карточки"`
	Description string     `json:"description,omitempty" jsonschema:"описание"`
	Deadline    string     `json:"deadline,omitempty" jsonschema:"срок: ГГГГ-ММ-ДД или ГГГГ-ММ-ДДTЧЧ:ММ (дата без времени = 18:00)"`
	Priority    int        `json:"priority,omitempty" jsonschema:"приоритет 1..10, по умолчанию 5"`
	PlanHours   *float64   `json:"plan_hours,omitempty" jsonschema:"план, часы"`
	FactHours   *float64   `json:"fact_hours,omitempty" jsonschema:"факт, часы"`
	Performers  []string   `json:"performers,omitempty" jsonschema:"исполнители: фамилии или Id сотрудников"`
	Tags        []string   `json:"tags,omitempty" jsonschema:"теги: названия существующих тегов доски"`
	Attachments []attachIn `json:"attachments,omitempty" jsonschema:"вложения: в каждом ровно одно из url, file, document_id"`
}

type updateTicketIn struct {
	ID          int64      `json:"id" jsonschema:"Id карточки (число из rx_tickets, не код вида ABC-12)"`
	Name        string     `json:"name,omitempty" jsonschema:"новое название; не передавайте, если менять не нужно"`
	Description string     `json:"description,omitempty" jsonschema:"новое описание целиком; не передавайте, если менять не нужно"`
	Deadline    string     `json:"deadline,omitempty" jsonschema:"новый срок: ГГГГ-ММ-ДД или ГГГГ-ММ-ДДTЧЧ:ММ"`
	Priority    int        `json:"priority,omitempty" jsonschema:"приоритет 1..10"`
	PlanHours   *float64   `json:"plan_hours,omitempty" jsonschema:"новый план, часы; не передавайте, если менять не нужно"`
	FactHours   *float64   `json:"fact_hours,omitempty" jsonschema:"новый факт, часы; не передавайте, если менять не нужно"`
	Performers  []string   `json:"performers,omitempty" jsonschema:"добавить исполнителей: фамилии или Id"`
	Tags        []string   `json:"tags,omitempty" jsonschema:"добавить теги: названия"`
	Column      string     `json:"column,omitempty" jsonschema:"перенести в колонку: название или Id"`
	Attachments []attachIn `json:"attachments,omitempty" jsonschema:"добавить вложения: в каждом ровно одно из url, file, document_id"`
}

type createColumnIn struct {
	Board    string `json:"board" jsonschema:"доска: название, префикс или Id (rx_boards)"`
	Name     string `json:"name" jsonschema:"название колонки"`
	Position int    `json:"position,omitempty" jsonschema:"место слева направо, 1 = первая; по умолчанию перед финальной колонкой («Выполнено»)"`
	IsFinal  bool   `json:"is_final,omitempty" jsonschema:"true = финальная: карточки в ней считаются закрытыми"`
	WipLimit int    `json:"wip_limit,omitempty" jsonschema:"лимит карточек в колонке, 0 = без лимита"`
}

type commentTicketIn struct {
	ID   int64  `json:"id" jsonschema:"числовой Id карточки: значение id в скобках в строках rx_tickets и rx_board; код вида ABC-12 не подходит"`
	Text string `json:"text" jsonschema:"текст комментария, обычный текст; виден всем участникам доски"`
}

// ticketComments дописывает к карточке блок комментариев. Сбой чтения комментариев
// карточку не роняет: модель получает карточку и причину, почему комментариев нет.
func (s *Server) ticketComments(ctx context.Context, t *rx.Ticket) string {
	if t.Comments == 0 {
		return ""
	}
	cs, err := s.svc.TicketComments(ctx, t)
	if err != nil {
		return fmt.Sprintf("\nКомментарии прочитать не удалось: %v", err)
	}
	if len(cs) == 0 {
		return ""
	}
	return "\n" + s.svc.F.Comments(cs)
}

type deleteTicketsIn struct {
	IDs []int64 `json:"ids" jsonschema:"Id карточек (числа из rx_board или rx_tickets, не коды вида ABC-12), до 100 за раз"`
}

const maxDeleteTickets = 100

func ticketLabel(t rx.Ticket) string {
	uid := t.UID
	if uid == "" {
		uid = fmt.Sprintf("#%d", t.ID)
	}
	name := []rune(t.Name)
	if len(name) > 60 {
		name = append(name[:57], []rune("…")...)
	}
	return fmt.Sprintf("%s «%s»", uid, string(name))
}

func ticketLabels(ts []rx.Ticket, max int) string {
	var parts []string
	for i, t := range ts {
		if i == max {
			parts = append(parts, fmt.Sprintf("и ещё %d", len(ts)-max))
			break
		}
		parts = append(parts, ticketLabel(t))
	}
	return strings.Join(parts, "; ")
}

func (s *Server) registerBoardWrite() {
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_create_column",
		Description: "Создать колонку на agile-доске: название, место, финальная или нет, лимит карточек. Без position колонка встаёт перед финальной («Выполнено»). Меняет доску сразу, удалить колонку этим сервером нельзя. Доску можно назвать словами или передать Id из rx_boards. Перед вызовом перескажите пользователю доску, название и место.",
		Annotations: rw("Создать колонку", false),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in createColumnIn) (*mcp.CallToolResult, any, error) {
		msg := fmt.Sprintf("Создать колонку «%s» на доске %s", in.Name, in.Board)
		if in.Position > 0 {
			msg += fmt.Sprintf(", место %d слева", in.Position)
		}
		if in.IsFinal {
			msg += ", финальная"
		}
		if in.WipLimit > 0 {
			msg += fmt.Sprintf(", лимит %d", in.WipLimit)
		}
		if ok, err := s.confirm(ctx, req, msg+"?"); err != nil || !ok {
			return declined(err)
		}
		b, col, pos, notes, err := s.svc.CreateColumn(ctx, rx.ColumnInput{
			Board: in.Board, Name: in.Name, Position: in.Position, IsFinal: in.IsFinal, WipLimit: in.WipLimit,
		})
		if err != nil {
			return fail(err)
		}
		s.log.Info("column created", "id", col.ID, "board", b.ID)
		out := fmt.Sprintf("Колонка «%s» (#%d) создана на доске «%s» (#%d), место %d слева.", col.Name, col.ID, b.Name, b.ID, pos)
		for _, n := range notes {
			out += "\nВнимание: " + n
		}
		return text(out + "\nДоска целиком: rx_board."), nil, nil
	})

	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_comment_ticket",
		Description: "Добавить комментарий к карточке agile-доски от имени пользователя. Комментарий виден всем участникам доски; изменить или удалить его этим сервером нельзя. Используйте, чтобы записать ход работы или ответить в обсуждении; поля карточки меняет rx_update_ticket, существующие комментарии показывает rx_ticket. Нужен числовой Id карточки из rx_tickets или rx_board. Перед вызовом покажите пользователю текст комментария.",
		Annotations: rw("Комментарий к карточке", false),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in commentTicketIn) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(in.Text) == "" {
			return fail(errors.New("пустой комментарий"))
		}
		t, err := s.svc.TicketByID(ctx, in.ID)
		if err != nil {
			return fail(err)
		}
		label := t.UID
		if label == "" {
			label = fmt.Sprintf("#%d", t.ID)
		}
		if ok, err := s.confirm(ctx, req, fmt.Sprintf("Добавить комментарий к карточке %s «%s»?", label, t.Name)); err != nil || !ok {
			return declined(err)
		}
		if err := s.svc.AddTicketComment(ctx, t, in.Text); err != nil {
			return fail(err)
		}
		s.log.Info("ticket comment added", "id", t.ID)
		out := fmt.Sprintf("Комментарий добавлен к карточке %s (id %d).", label, t.ID)
		if cs, err := s.svc.TicketComments(ctx, t); err == nil && len(cs) > 0 {
			out += "\n" + s.svc.F.Comments(cs)
		}
		return text(out), nil, nil
	})

	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_delete_tickets",
		Description: "Удалить карточки с agile-доски так же, как это делает кнопка удаления в интерфейсе: карточка уходит с доски и получает статус Deleted. Вернуть её этим сервером нельзя. Нужны числовые Id карточек из rx_board или rx_tickets, не коды; до 100 за вызов. Перед вызовом перечислите пользователю, какие карточки будут удалены.",
		Annotations: rw("Удалить карточки с доски", true),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in deleteTicketsIn) (*mcp.CallToolResult, any, error) {
		if len(in.IDs) > maxDeleteTickets {
			return fail(fmt.Errorf("за раз не больше %d карточек, передано %d", maxDeleteTickets, len(in.IDs)))
		}
		ts, err := s.svc.TicketsByIDs(ctx, in.IDs)
		if err != nil {
			return fail(err)
		}
		msg := fmt.Sprintf("Удалить с доски карточки (%d): %s?", len(ts), ticketLabels(ts, 10))
		if ok, err := s.confirm(ctx, req, msg); err != nil || !ok {
			return declined(err)
		}
		res, err := s.svc.RemoveTickets(ctx, ts)
		if res != nil {
			s.log.Info("tickets removed", "removed", len(res.Removed), "blocked", len(res.Blocked))
		}
		var b strings.Builder
		if res != nil && len(res.Removed) > 0 {
			fmt.Fprintf(&b, "Удалено с доски: %d — %s\n", len(res.Removed), ticketLabels(res.Removed, 50))
		}
		if res != nil && len(res.Blocked) > 0 {
			fmt.Fprintf(&b, "Не удалено, карточка заблокирована: %s\n", ticketLabels(res.Blocked, 50))
		}
		if res != nil && len(res.NotOnBoard) > 0 {
			fmt.Fprintf(&b, "Уже не на доске: %s\n", ticketLabels(res.NotOnBoard, 50))
		}
		if res != nil {
			for _, w := range res.Warnings {
				fmt.Fprintf(&b, "Предупреждение доски: %s\n", w)
			}
		}
		if err != nil {
			fmt.Fprintf(&b, "Ошибка: %v", err)
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: strings.TrimSpace(b.String())}}}, nil, nil
		}
		return text(strings.TrimSpace(b.String())), nil, nil
	})

	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_create_ticket",
		Description: "Создать карточку на agile-доске. Доску, колонку, исполнителей и теги можно называть словами, сервер сопоставит их сам; теги должны уже существовать на доске. План и факт в часах задаются в plan_hours и fact_hours. В attachments можно сразу приложить ссылки, документы RX по Id и файлы с диска. Без column карточка попадает в первую колонку. Меняет данные сразу. Для поручения с контролем срока в самой системе используйте rx_create_simple_task. Перед вызовом перескажите пользователю доску, колонку, название и срок.",
		Annotations: rw("Создать карточку", false),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in createTicketIn) (*mcp.CallToolResult, any, error) {
		if err := checkHours(in.PlanHours, in.FactHours); err != nil {
			return fail(err)
		}
		var dl *time.Time
		if in.Deadline != "" {
			t, err := parseDeadline(in.Deadline, s.svc.F.Loc)
			if err != nil {
				return fail(err)
			}
			dl = &t
		}
		msg := fmt.Sprintf("Создать карточку «%s» на доске %s", in.Name, in.Board)
		if in.Column != "" {
			msg += ", колонка " + in.Column
		}
		if dl != nil {
			msg += ", срок " + s.svc.F.DateTime(dl)
		}
		if in.PlanHours != nil {
			msg += ", план " + hours(*in.PlanHours)
		}
		if in.FactHours != nil {
			msg += ", факт " + hours(*in.FactHours)
		}
		atts, attLabels := attachInputs(in.Attachments)
		if len(atts) > 0 {
			msg += ", вложения: " + strings.Join(attLabels, "; ")
		}
		if ok, err := s.confirm(ctx, req, msg+"?"); err != nil || !ok {
			return declined(err)
		}
		t, b, col, notes, err := s.svc.CreateTicket(ctx, rx.TicketInput{
			Board: in.Board, Column: in.Column, Name: in.Name, Description: in.Description,
			Deadline: dl, Priority: in.Priority, PlanHours: in.PlanHours, FactHours: in.FactHours,
			Performers: in.Performers, Tags: in.Tags, Attachments: atts,
		})
		if err != nil {
			return fail(err)
		}
		s.log.Info("ticket created", "id", t.ID, "board", b.ID)
		var sb strings.Builder
		uid := t.UID
		if uid == "" {
			uid = fmt.Sprintf("#%d", t.ID)
		}
		fmt.Fprintf(&sb, "Карточка %s (id %d) создана на доске «%s», колонка «%s».\n", uid, t.ID, b.Name, col.Name)
		sb.WriteString(s.svc.F.TicketCard(*t))
		for _, n := range notes {
			fmt.Fprintf(&sb, "\nВнимание: %s", n)
		}
		return text(sb.String()), nil, nil
	})

	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_update_ticket",
		Description: "Изменить карточку на agile-доске: название, описание, срок, приоритет, план и факт в часах, добавить исполнителей, теги и вложения (ссылка, документ RX по Id, файл с диска) или перенести в другую колонку. Меняются только переданные поля, остальные остаются. Убрать исполнителя, тег или вложение этим инструментом нельзя. Нужен числовой Id карточки из rx_tickets или rx_board. Меняет данные сразу.",
		Annotations: rw("Изменить карточку", false),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in updateTicketIn) (*mcp.CallToolResult, any, error) {
		if err := checkHours(in.PlanHours, in.FactHours); err != nil {
			return fail(err)
		}
		cur, err := s.svc.TicketByID(ctx, in.ID)
		if err != nil {
			return fail(err)
		}
		var dl *time.Time
		if in.Deadline != "" {
			t, e := parseDeadline(in.Deadline, s.svc.F.Loc)
			if e != nil {
				return fail(e)
			}
			dl = &t
		}
		var what []string
		if in.Name != "" {
			what = append(what, "название")
		}
		if in.Description != "" {
			what = append(what, "описание")
		}
		if dl != nil {
			what = append(what, "срок на "+s.svc.F.DateTime(dl))
		}
		if in.Priority > 0 {
			what = append(what, fmt.Sprintf("приоритет %d", in.Priority))
		}
		if in.PlanHours != nil {
			what = append(what, "план "+hours(*in.PlanHours))
		}
		if in.FactHours != nil {
			what = append(what, "факт "+hours(*in.FactHours))
		}
		if len(in.Performers) > 0 {
			what = append(what, "исполнителей: "+strings.Join(in.Performers, ", "))
		}
		if len(in.Tags) > 0 {
			what = append(what, "теги: "+strings.Join(in.Tags, ", "))
		}
		if in.Column != "" {
			what = append(what, "перенос в колонку "+in.Column)
		}
		atts, attLabels := attachInputs(in.Attachments)
		if len(atts) > 0 {
			what = append(what, "вложения: "+strings.Join(attLabels, "; "))
		}
		if len(what) == 0 {
			return fail(errors.New("не указано ни одного изменения"))
		}
		uid := cur.UID
		if uid == "" {
			uid = fmt.Sprintf("#%d", cur.ID)
		}
		if ok, err := s.confirm(ctx, req, fmt.Sprintf("Изменить карточку %s «%s»: %s?", uid, cur.Name, strings.Join(what, ", "))); err != nil || !ok {
			return declined(err)
		}
		t, notes, err := s.svc.UpdateTicket(ctx, in.ID, rx.TicketInput{
			Name: in.Name, Description: in.Description, Deadline: dl, Priority: in.Priority,
			PlanHours: in.PlanHours, FactHours: in.FactHours,
			Performers: in.Performers, Tags: in.Tags, Attachments: atts,
		}, in.Column)
		if err != nil {
			return fail(err)
		}
		s.log.Info("ticket updated", "id", in.ID)
		out := s.svc.F.TicketCard(*t)
		for _, n := range notes {
			out += "\nВнимание: " + n
		}
		return text(out), nil, nil
	})
}

// checkHours отсекает отрицательные часы: доска их примет, а отчёты по времени нет.
func checkHours(plan, fact *float64) error {
	if plan != nil && *plan < 0 {
		return errors.New("plan_hours не может быть отрицательным")
	}
	if fact != nil && *fact < 0 {
		return errors.New("fact_hours не может быть отрицательным")
	}
	return nil
}

// hours печатает часы без лишних нулей: 4 ч, 1.5 ч.
func hours(h float64) string {
	return strconv.FormatFloat(h, 'f', -1, 64) + " ч"
}
