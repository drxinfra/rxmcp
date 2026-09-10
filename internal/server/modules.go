package server

import (
	"context"
	"fmt"
	"strings"

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
