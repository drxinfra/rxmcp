package rx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/drxinfra/rxmcp/internal/odata"
)

// --- База знаний (модуль «Знания»: статьи = документы с markdown-версиями) ---

// Area область знаний.
type Area struct {
	ID              int64  `json:"Id"`
	Name            string `json:"Name"`
	Status          string `json:"Status"`
	RootArticleID   int64  `json:"RootArticleId"`
	RootArticleName string `json:"RootArticleName"`
	IsDefaultArea   bool   `json:"IsDefaultArea"`
}

// Article статья базы знаний.
type Article struct {
	ID             int64      `json:"Id"`
	Name           string     `json:"Name"`
	LifeCycleState string     `json:"LifeCycleState"`
	Modified       *time.Time `json:"Modified"`
	Created        *time.Time `json:"Created"`
	HasVersions    bool       `json:"HasVersions"`
	Author         *Ref       `json:"Author"`
	Areas          []struct {
		Area *Ref `json:"Area"`
	} `json:"Areas"`
	Tags []struct {
		Tag *Ref `json:"Tag"`
	} `json:"Tags"`
	Versions []Version `json:"Versions"`
}

// KBAreas области знаний.
func (s *Service) KBAreas(ctx context.Context, q string) ([]Area, error) {
	filter := "Status eq 'Active'"
	if q = strings.TrimSpace(q); q != "" {
		filter += " and contains(Name," + odata.Quote(q) + ")"
	}
	page, err := s.c.List(ctx, "IAreaOfExpertises", odata.Query{
		Filter: filter, Select: "Id,Name,Status,RootArticleId,RootArticleName,IsDefaultArea", OrderBy: "Name asc", Top: s.maxPage,
	})
	if err != nil {
		return nil, err
	}
	return decodeList[Area](page)
}

// ArticleFilter параметры поиска статей.
type ArticleFilter struct {
	Query   string
	AreaID  int64
	Tag     string
	Author  string
	Limit   int
	AllStat bool // включая черновики и устаревшие
}

// KBFindArticles поиск статей по названию и области.
func (s *Service) KBFindArticles(ctx context.Context, fl ArticleFilter) ([]Article, *int64, error) {
	var parts []string
	if q := strings.TrimSpace(fl.Query); q != "" {
		parts = append(parts, "contains(Name,"+odata.Quote(q)+")")
	}
	if fl.AreaID != 0 {
		parts = append(parts, fmt.Sprintf("Areas/any(a: a/Area/Id eq %d)", fl.AreaID))
	}
	if fl.Tag != "" {
		parts = append(parts, "Tags/any(t: contains(t/Tag/Name,"+odata.Quote(fl.Tag)+"))")
	}
	if fl.Author != "" {
		parts = append(parts, "contains(Author/Name,"+odata.Quote(fl.Author)+")")
	}
	if len(parts) == 0 {
		return nil, nil, errors.New("задайте условие: query, area_id, tag или author")
	}
	if !fl.AllStat {
		parts = append(parts, "LifeCycleState eq 'Active'")
	}
	page, err := s.c.List(ctx, "IMemoArticles", odata.Query{
		Filter:  strings.Join(parts, " and "),
		Select:  "Id,Name,LifeCycleState,Modified,HasVersions",
		Expand:  "Author($select=Id,Name),Areas($expand=Area($select=Id,Name))",
		OrderBy: "Modified desc",
		Top:     s.limit(fl.Limit),
		Count:   true,
	})
	if err != nil {
		return nil, nil, err
	}
	items, err := decodeList[Article](page)
	return items, page.Count, err
}

// KBArticle статья с телом (markdown).
func (s *Service) KBArticle(ctx context.Context, id int64) (*Article, string, error) {
	var a Article
	if err := s.c.Get(ctx, "IMemoArticles", id, odata.Query{
		Select: "Id,Name,LifeCycleState,Modified,Created,HasVersions",
		Expand: "Author($select=Id,Name),Areas($expand=Area($select=Id,Name)),Tags($expand=Tag($select=Id,Name)),Versions($select=Id,Number,Created,IsHidden;$expand=AssociatedApplication($select=Extension))",
	}, &a); err != nil {
		return nil, "", err
	}
	if len(a.Versions) == 0 {
		return &a, "", nil
	}
	sort.Slice(a.Versions, func(i, j int) bool { return a.Versions[i].Number < a.Versions[j].Number })
	v := a.Versions[len(a.Versions)-1]
	data, _, err := s.c.Raw(ctx, fmt.Sprintf("IElectronicDocuments(%d)/Versions(%d)/Body/$value", id, v.ID))
	if err != nil {
		return &a, "", err
	}
	return &a, string(data), nil
}

// --- Agile-доски ---

// Board доска.
type Board struct {
	ID      int64      `json:"Id"`
	Name    string     `json:"Name"`
	Prefix  string     `json:"Prefix"`
	Status  string     `json:"Status"`
	Created *time.Time `json:"Created"`
	Owner   *Ref       `json:"Owner"`
	Project *Ref       `json:"Project"`
}

// Ticket карточка на доске.
type Ticket struct {
	ID           int64      `json:"Id"`
	Name         string     `json:"Name"`
	UID          string     `json:"Uid"`
	Number       int        `json:"Number"`
	Status       string     `json:"Status"`
	Priority     *int       `json:"Priority"`
	Deadline     *time.Time `json:"Deadline"`
	BeginDate    *time.Time `json:"BeginDate"`
	CreateDate   *time.Time `json:"CreateDate"`
	CompleteDate *time.Time `json:"CompleteDate"`
	Description  string     `json:"Description"`
	Laborious    *float64   `json:"Laboriousness"`
	Elapsed      *float64   `json:"ElapsedTime"`
	Votes        int        `json:"Votes"`
	Comments     int        `json:"CommentsCount"`
	BoardID      int64      `json:"BoardId"`
	Author       *Ref       `json:"Author"`
	Performers   []struct {
		Performer *Ref `json:"Performer"`
	} `json:"Performers"`
	TicketsTags []struct {
		Tag *Ref `json:"TicketTag"`
	} `json:"TicketsTags"`
	Attachments []struct {
		Name string `json:"Name"`
		URL  string `json:"Url"`
	} `json:"Attachments"`
}

// Column колонка доски с карточками.
type Column struct {
	ID       int64  `json:"Id"`
	Name     string `json:"Name"`
	IsFinal  bool   `json:"IsFinal"`
	WipLimit *int   `json:"WipLimit"`
	Status   string `json:"Status"`
	Tickets  []struct {
		Position int     `json:"Position"`
		Ticket   *Ticket `json:"Ticket"`
	} `json:"Tickets"`
}

// Boards список досок.
func (s *Service) Boards(ctx context.Context, q string, all bool, limit int) ([]Board, error) {
	var parts []string
	if !all {
		parts = append(parts, "Status eq 'Active'")
	}
	if q = strings.TrimSpace(q); q != "" {
		parts = append(parts, "(contains(Name,"+odata.Quote(q)+") or contains(Prefix,"+odata.Quote(strings.ToUpper(q))+"))")
	}
	page, err := s.c.List(ctx, "IBoards", odata.Query{
		Filter: strings.Join(parts, " and "), Select: "Id,Name,Prefix,Status,Created",
		Expand: "Owner($select=Id,Name),Project($select=Id,Name)", OrderBy: "Name asc", Top: s.limit(limit),
	})
	if err != nil {
		return nil, err
	}
	return decodeList[Board](page)
}

// BoardByID доска c колонками и карточками.
func (s *Service) BoardByID(ctx context.Context, id int64) (*Board, []Column, error) {
	var b Board
	if err := s.c.Get(ctx, "IBoards", id, odata.Query{
		Select: "Id,Name,Prefix,Status,Created", Expand: "Owner($select=Id,Name),Project($select=Id,Name)",
	}, &b); err != nil {
		return nil, nil, err
	}
	page, err := s.c.List(ctx, "IColumns", odata.Query{
		Filter: fmt.Sprintf("BoardId eq %d and Status eq 'Active'", id),
		Select: "Id,Name,IsFinal,WipLimit,Status",
		Expand: "Tickets($select=Id,Position;$expand=Ticket($select=Id,Name,Uid,Number,Status,Priority,Deadline,CompleteDate;$expand=Performers($expand=Performer($select=Id,Name))))",
		Top:    50,
	})
	if err != nil {
		return &b, nil, err
	}
	cols, err := decodeList[Column](page)
	if err != nil {
		return &b, nil, err
	}
	// Порядок колонок хранится в доске (IndexColumn); берём его отдельным запросом, иначе по Id.
	order := map[int64]int{}
	var bc struct {
		Columns []struct {
			IndexColumn int `json:"IndexColumn"`
			Column      Ref `json:"Column"`
		} `json:"Columns"`
	}
	if err := s.c.Get(ctx, "IBoards", id, odata.Query{Select: "Id", Expand: "Columns($select=IndexColumn;$expand=Column($select=Id))"}, &bc); err == nil {
		for _, c := range bc.Columns {
			order[c.Column.ID] = c.IndexColumn
		}
	}
	sort.SliceStable(cols, func(i, j int) bool {
		oi, oj := order[cols[i].ID], order[cols[j].ID]
		if oi != oj {
			return oi < oj
		}
		return cols[i].ID < cols[j].ID
	})
	for i := range cols {
		// Дубли ссылок на одну карточку встречаются, оставляем первую.
		seen := map[int64]bool{}
		uniq := cols[i].Tickets[:0]
		sort.SliceStable(cols[i].Tickets, func(a, b int) bool { return cols[i].Tickets[a].Position < cols[i].Tickets[b].Position })
		for _, t := range cols[i].Tickets {
			if t.Ticket == nil || seen[t.Ticket.ID] {
				continue
			}
			seen[t.Ticket.ID] = true
			uniq = append(uniq, t)
		}
		cols[i].Tickets = uniq
	}
	return &b, cols, nil
}

// TicketByID карточка.
func (s *Service) TicketByID(ctx context.Context, id int64) (*Ticket, error) {
	var t Ticket
	if err := s.c.Get(ctx, "ITickets", id, odata.Query{
		Expand: "Author($select=Id,Name),Performers($expand=Performer($select=Id,Name)),TicketsTags($expand=TicketTag($select=Id,Name)),Attachments",
	}, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// FindTickets карточки по подстроке названия или коду (например DDKRA-12).
func (s *Service) FindTickets(ctx context.Context, q string, boardID int64, status string, limit int) ([]Ticket, *int64, error) {
	var parts []string
	q = strings.TrimSpace(q)
	if q != "" {
		parts = append(parts, "(contains(Name,"+odata.Quote(q)+") or Uid eq "+odata.Quote(strings.ToUpper(q))+")")
	}
	if boardID != 0 {
		parts = append(parts, fmt.Sprintf("BoardId eq %d", boardID))
	}
	switch strings.ToLower(status) {
	case "", "active", "open":
		parts = append(parts, "Status eq 'Active'")
	case "closed":
		parts = append(parts, "Status eq 'Closed'")
	case "all":
	default:
		return nil, nil, errors.New("status: active, closed или all")
	}
	if len(parts) == 0 {
		return nil, nil, errors.New("задайте query или board_id")
	}
	page, err := s.c.List(ctx, "ITickets", odata.Query{
		Filter:  strings.Join(parts, " and "),
		Select:  "Id,Name,Uid,Number,Status,Priority,Deadline,CompleteDate,BoardId",
		Expand:  "Performers($expand=Performer($select=Id,Name))",
		OrderBy: "CreateDate desc", Top: s.limit(limit), Count: true,
	})
	if err != nil {
		return nil, nil, err
	}
	items, err := decodeList[Ticket](page)
	return items, page.Count, err
}

// --- Проекты и планы ---

// Project проект (карточка).
type Project struct {
	ID               int64      `json:"Id"`
	Name             string     `json:"Name"`
	ShortName        string     `json:"ShortName"`
	Status           string     `json:"Status"`
	Stage            string     `json:"Stage"`
	StatusIssues     string     `json:"StatusIssues"`
	Priority         string     `json:"Priority"`
	Description      string     `json:"Description"`
	Note             string     `json:"Note"`
	StartDate        *time.Time `json:"StartDate"`
	EndDate          *time.Time `json:"EndDate"`
	ActualStartDate  *time.Time `json:"ActualStartDate"`
	ActualFinishDate *time.Time `json:"ActualFinishDate"`
	ExecutionPercent *int       `json:"ExecutionPercent"`
	Modified         *time.Time `json:"Modified"`
	Manager          *Ref       `json:"Manager"`
	Administrator    *Ref       `json:"Administrator"`
	InternalCustomer *Ref       `json:"InternalCustomer"`
	ExternalCustomer *Ref       `json:"ExternalCustomer"`
	ProjectKind      *Ref       `json:"ProjectKind"`
	LeadingProject   *Ref       `json:"LeadingProject"`
	TeamMembers      []struct {
		Group  string `json:"Group"`
		Member *Ref   `json:"Member"`
	} `json:"TeamMembers"`
	Gates []struct {
		PlanDate   *time.Time `json:"PlanDate"`
		ActualDate *time.Time `json:"ActualDate"`
		IsPassed   bool       `json:"IsPassed"`
		Level      string     `json:"Level"`
		Gate       *Ref       `json:"Gate"`
	} `json:"GatesDirRX"`
	Type string `json:"@odata.type"`
}

// ProjectFilter параметры списка проектов.
type ProjectFilter struct {
	Query   string
	Manager string
	Stage   string
	Member  bool // только где я в команде или руководитель
	All     bool // включая закрытые
	Limit   int
}

// Projects список проектов.
func (s *Service) Projects(ctx context.Context, fl ProjectFilter) ([]Project, *int64, error) {
	var parts []string
	if !fl.All {
		parts = append(parts, "Status eq 'Active'")
	}
	if q := strings.TrimSpace(fl.Query); q != "" {
		parts = append(parts, "(contains(Name,"+odata.Quote(q)+") or contains(ShortName,"+odata.Quote(q)+"))")
	}
	if fl.Manager != "" {
		parts = append(parts, "contains(Manager/Name,"+odata.Quote(fl.Manager)+")")
	}
	if fl.Stage != "" {
		parts = append(parts, "Stage eq "+odata.Quote(fl.Stage))
	}
	if fl.Member {
		me, err := s.WhoAmI(ctx)
		if err != nil {
			return nil, nil, err
		}
		parts = append(parts, fmt.Sprintf("(Manager/Id eq %d or Administrator/Id eq %d or TeamMembers/any(m: m/Member/Id eq %d))", me.ID, me.ID, me.ID))
	}
	page, err := s.c.List(ctx, "IProjectCores", odata.Query{
		Filter:  strings.Join(parts, " and "),
		Select:  "Id,Name,ShortName,Status,Stage,StatusIssues,Priority,StartDate,EndDate,ExecutionPercent,Modified",
		Expand:  "Manager($select=Id,Name),ProjectKind($select=Id,Name)",
		OrderBy: "Modified desc",
		Top:     s.limit(fl.Limit),
		Count:   true,
	})
	if err != nil {
		return nil, nil, err
	}
	items, err := decodeList[Project](page)
	return items, page.Count, err
}

// ProjectByID карточка проекта с командой, гейтами и планами.
func (s *Service) ProjectByID(ctx context.Context, id int64) (*Project, []Document, error) {
	var p Project
	if err := s.c.Get(ctx, "IProjectCores", id, odata.Query{
		Select: "Id,Name,ShortName,Status,Stage,StatusIssues,Priority,Description,Note,StartDate,EndDate,ActualStartDate,ActualFinishDate,ExecutionPercent,Modified",
		Expand: "Manager($select=Id,Name),Administrator($select=Id,Name),InternalCustomer($select=Id,Name),ExternalCustomer($select=Id,Name),ProjectKind($select=Id,Name),LeadingProject($select=Id,Name)," +
			"TeamMembers($select=Group;$expand=Member($select=Id,Name)),GatesDirRX($select=PlanDate,ActualDate,IsPassed,Level;$expand=Gate($select=Id,Name))",
	}, &p); err != nil {
		return nil, nil, err
	}
	var plans []Document
	if page, err := s.c.List(ctx, "IProjectPlanRXs", odata.Query{
		Filter: fmt.Sprintf("Project/Id eq %d", id), Select: "Id,Name,LifeCycleState,Modified", OrderBy: "Modified desc", Top: 20,
	}); err == nil {
		plans, _ = decodeList[Document](page)
	}
	return &p, plans, nil
}

// Plan план проекта (документ с моделью в теле версии).
type Plan struct {
	Document
	Status           string     `json:"Status"`
	StartDate        *time.Time `json:"StartDate"`
	EndDate          *time.Time `json:"EndDate"`
	ActualStartDate  *time.Time `json:"ActualStartDate"`
	ActualFinishDate *time.Time `json:"ActualFinishDate"`
	ExecutionPercent *int       `json:"ExecutionPercent"`
	Project          *Ref       `json:"Project"`
	TeamMembers      []struct {
		Group  string `json:"Group"`
		Member *Ref   `json:"Member"`
	} `json:"TeamMembers"`
}

// FindPlans планы проектов по названию или проекту.
func (s *Service) FindPlans(ctx context.Context, q string, projectID int64, limit int) ([]Plan, *int64, error) {
	var parts []string
	if q = strings.TrimSpace(q); q != "" {
		parts = append(parts, "contains(Name,"+odata.Quote(q)+")")
	}
	if projectID != 0 {
		parts = append(parts, fmt.Sprintf("Project/Id eq %d", projectID))
	}
	if len(parts) == 0 {
		return nil, nil, errors.New("задайте query или project_id")
	}
	page, err := s.c.List(ctx, "IProjectPlanRXs", odata.Query{
		Filter: strings.Join(parts, " and "), Select: "Id,Name,LifeCycleState,Status,Modified,StartDate,EndDate,ExecutionPercent",
		Expand: "Project($select=Id,Name),Author($select=Id,Name)", OrderBy: "Modified desc", Top: s.limit(limit), Count: true,
	})
	if err != nil {
		return nil, nil, err
	}
	items, err := decodeList[Plan](page)
	return items, page.Count, err
}

// PlanActivity работа плана (из JSON-модели).
type PlanActivity struct {
	ID            int64    `json:"Id"`
	Name          string   `json:"Name"`
	Type          string   `json:"TypeActivity"`
	ResponsibleID *int64   `json:"ResponsibleId"`
	Note          string   `json:"Note"`
	SortIndex     int      `json:"SortIndex"`
	LeadID        *int64   `json:"LeadActivityId"`
	Start         string   `json:"StartDate"`
	End           string   `json:"EndDate"`
	GateID        *int64   `json:"GateId"`
	State         any      `json:"State"`
	Execution     any      `json:"Execution"`
	DisplayData   any      `json:"DisplayData"`
	BaselineWork  *float64 `json:"BaselineWork"`
	Predecessors  []any    `json:"Predecessors"`
	Resources     []any    `json:"Resources"`
}

// PlanModel модель плана.
type PlanModel struct {
	NumberVersion int `json:"NumberVersion"`
	Project       struct {
		Name      string `json:"Name"`
		StartDate string `json:"StartDate"`
		EndDate   string `json:"EndDate"`
	} `json:"Project"`
	Activities []PlanActivity `json:"Activities"`
	Risks      []any          `json:"Risks"`
}

// PlanByID план: карточка и модель работ.
func (s *Service) PlanByID(ctx context.Context, id int64) (*Plan, *PlanModel, map[int64]string, error) {
	var p Plan
	if err := s.c.Get(ctx, "IProjectPlanRXs", id, odata.Query{
		Select: "Id,Name,LifeCycleState,Status,Created,Modified,StartDate,EndDate,ActualStartDate,ActualFinishDate,ExecutionPercent",
		Expand: "Project($select=Id,Name),Author($select=Id,Name),TeamMembers($select=Group;$expand=Member($select=Id,Name)),Versions($select=Id,Number,Created,IsHidden)",
	}, &p); err != nil {
		return nil, nil, nil, err
	}
	if len(p.Versions) == 0 {
		return &p, nil, nil, nil
	}
	sort.Slice(p.Versions, func(i, j int) bool { return p.Versions[i].Number < p.Versions[j].Number })
	v := p.Versions[len(p.Versions)-1]
	data, _, err := s.c.Raw(ctx, fmt.Sprintf("IElectronicDocuments(%d)/Versions(%d)/Body/$value", id, v.ID))
	if err != nil {
		return &p, nil, nil, err
	}
	var m PlanModel
	if err := json.Unmarshal(data, &m); err != nil {
		return &p, nil, nil, fmt.Errorf("модель плана не разобрана: %w", err)
	}
	// Имена ответственных одним запросом.
	ids := map[int64]bool{}
	for _, a := range m.Activities {
		if a.ResponsibleID != nil && *a.ResponsibleID != 0 {
			ids[*a.ResponsibleID] = true
		}
	}
	names := map[int64]string{}
	if len(ids) > 0 {
		var parts []string
		for id := range ids {
			parts = append(parts, fmt.Sprintf("Id eq %d", id))
			if len(parts) >= 40 {
				break
			}
		}
		if page, err := s.c.List(ctx, "IRecipients", odata.Query{Filter: strings.Join(parts, " or "), Select: "Id,Name", Top: 40}); err == nil {
			for _, raw := range page.Value {
				var r Ref
				if json.Unmarshal(raw, &r) == nil {
					names[r.ID] = r.Name
				}
			}
		}
	}
	return &p, &m, names, nil
}

func decodeList[T any](page *odata.Page) ([]T, error) {
	out := make([]T, 0, len(page.Value))
	for _, raw := range page.Value {
		var v T
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("ответ RX не разобран: %w", err)
		}
		out = append(out, v)
	}
	return out, nil
}
