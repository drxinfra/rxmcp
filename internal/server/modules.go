package server

import (
	"context"
	"errors"
	"fmt"
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
	Limit  int    `json:"limit,omitempty"`
}

type kbArticleIn struct {
	ID       int64 `json:"id" jsonschema:"Id статьи"`
	MaxChars int   `json:"max_chars,omitempty"`
	Offset   int   `json:"offset,omitempty"`
}

type boardsIn struct {
	Query string `json:"query,omitempty" jsonschema:"подстрока названия или префикс доски"`
	All   bool   `json:"include_closed,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type ticketsIn struct {
	Query   string `json:"query,omitempty" jsonschema:"подстрока названия или код карточки вида ABC-12"`
	BoardID int64  `json:"board_id,omitempty"`
	Status  string `json:"status,omitempty" jsonschema:"active (по умолчанию), closed, all"`
	Limit   int    `json:"limit,omitempty"`
}

type projectsIn struct {
	Query   string `json:"query,omitempty" jsonschema:"подстрока названия или краткого имени"`
	Manager string `json:"manager,omitempty" jsonschema:"подстрока имени руководителя"`
	Stage   string `json:"stage,omitempty" jsonschema:"Initiation, Planning, Execution, Closing"`
	Mine    bool   `json:"mine,omitempty" jsonschema:"true = только где я руководитель, администратор или в команде"`
	All     bool   `json:"include_closed,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type plansIn struct {
	Query     string `json:"query,omitempty" jsonschema:"подстрока названия плана"`
	ProjectID int64  `json:"project_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type planIn struct {
	ID      int64 `json:"id" jsonschema:"Id плана проекта (документа)"`
	MaxRows int   `json:"max_rows,omitempty" jsonschema:"сколько работ показать, по умолчанию 150"`
}

func (s *Server) registerModules() {
	// База знаний
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_kb_areas",
		Description: "Области базы знаний Directum RX (модуль «Знания»): Id, название, стартовая статья. С Id области можно искать статьи.",
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
		Description: "Поиск статей базы знаний по названию, области, тегу или автору. Возвращает Id, название, области, автора, дату. Текст статьи: rx_kb_article.",
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
		Description: "Статья базы знаний целиком: области, теги, автор и текст в markdown. Длинный текст обрезается по max_chars, продолжение через offset.",
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
		Description: "Agile-доски Directum RX: Id, название, префикс карточек, владелец, проект. Содержимое доски: rx_board.",
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
		Description: "Доска целиком: колонки по порядку и карточки в них с исполнителями и сроками. Нужен Id доски.",
		Annotations: ro("Доска"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		bd, cols, err := s.svc.BoardByID(ctx, in.ID)
		if err != nil {
			return fail(err)
		}
		return text(s.svc.F.BoardCard(*bd, cols)), nil, nil
	})
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_tickets",
		Description: "Поиск карточек на agile-досках по названию или коду (например ABC-12), по доске и статусу. Карточка целиком: rx_ticket.",
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
		Description: "Карточка agile-доски целиком: статус, исполнители, сроки, трудоёмкость, теги, вложения, описание. Нужен Id карточки (число, не код).",
		Annotations: ro("Карточка"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		t, err := s.svc.TicketByID(ctx, in.ID)
		if err != nil {
			return fail(err)
		}
		return text(s.svc.F.TicketCard(*t)), nil, nil
	})

	// Проекты и планы
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_projects",
		Description: "Проекты и инициативы Directum RX: стадия, состояние, руководитель, сроки, процент. mine=true покажет только мои. Карточка: rx_project.",
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
		Description: "Карточка проекта: стадия, сроки план и факт, руководитель, заказчик, команда по группам, гейты, описание и список планов проекта.",
		Annotations: ro("Проект"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, any, error) {
		p, plans, err := s.svc.ProjectByID(ctx, in.ID)
		if err != nil {
			return fail(err)
		}
		return text(s.svc.F.ProjectCard(*p, plans)), nil, nil
	})
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_project_plans",
		Description: "Поиск планов проектов по названию или Id проекта. Работы плана: rx_project_plan.",
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
		Description: "План проекта с деревом работ: разделы, работы, вехи, сроки, ответственные, проценты, просрочки. Нужен Id плана (документа).",
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

type createTicketIn struct {
	Board       string   `json:"board" jsonschema:"доска: название, префикс или Id (rx_boards)"`
	Column      string   `json:"column,omitempty" jsonschema:"колонка: название или Id; по умолчанию первая колонка доски"`
	Name        string   `json:"name" jsonschema:"название карточки"`
	Description string   `json:"description,omitempty" jsonschema:"описание"`
	Deadline    string   `json:"deadline,omitempty" jsonschema:"срок: ГГГГ-ММ-ДД или ГГГГ-ММ-ДДTЧЧ:ММ (дата без времени = 18:00)"`
	Priority    int      `json:"priority,omitempty" jsonschema:"приоритет 1..10, по умолчанию 5"`
	Performers  []string `json:"performers,omitempty" jsonschema:"исполнители: фамилии или Id сотрудников"`
	Tags        []string `json:"tags,omitempty" jsonschema:"теги: названия существующих тегов доски"`
}

type updateTicketIn struct {
	ID          int64    `json:"id" jsonschema:"Id карточки (число из rx_tickets, не код вида ABC-12)"`
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
	Deadline    string   `json:"deadline,omitempty" jsonschema:"новый срок: ГГГГ-ММ-ДД или ГГГГ-ММ-ДДTЧЧ:ММ"`
	Priority    int      `json:"priority,omitempty" jsonschema:"приоритет 1..10"`
	Performers  []string `json:"performers,omitempty" jsonschema:"добавить исполнителей: фамилии или Id"`
	Tags        []string `json:"tags,omitempty" jsonschema:"добавить теги: названия"`
	Column      string   `json:"column,omitempty" jsonschema:"перенести в колонку: название или Id"`
}

func (s *Server) registerBoardWrite() {
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name: "rx_create_ticket",
		Description: "Создать карточку на agile-доске Directum RX. Доску, колонку, исполнителей и теги можно называть словами, " +
			"они будут сопоставлены сами. Теги должны уже существовать на доске, новые этот интерфейс не заводит. " +
			"Перед вызовом перескажите пользователю доску, колонку, название и срок.",
		Annotations: rw("Создать карточку", false),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in createTicketIn) (*mcp.CallToolResult, any, error) {
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
		if ok, err := s.confirm(ctx, req, msg+"?"); err != nil || !ok {
			return declined(err)
		}
		t, b, col, notes, err := s.svc.CreateTicket(ctx, rx.TicketInput{
			Board: in.Board, Column: in.Column, Name: in.Name, Description: in.Description,
			Deadline: dl, Priority: in.Priority, Performers: in.Performers, Tags: in.Tags,
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
		Name: "rx_update_ticket",
		Description: "Изменить карточку на agile-доске: название, описание, срок, приоритет, а также добавить исполнителей и теги " +
			"или перенести карточку в другую колонку. Переданные поля меняются, остальные остаются как были.",
		Annotations: rw("Изменить карточку", false),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in updateTicketIn) (*mcp.CallToolResult, any, error) {
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
		if len(in.Performers) > 0 {
			what = append(what, "исполнителей: "+strings.Join(in.Performers, ", "))
		}
		if len(in.Tags) > 0 {
			what = append(what, "теги: "+strings.Join(in.Tags, ", "))
		}
		if in.Column != "" {
			what = append(what, "перенос в колонку "+in.Column)
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
			Performers: in.Performers, Tags: in.Tags,
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
