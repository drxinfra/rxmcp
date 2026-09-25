package rx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/drxinfra/rxmcp/internal/odata"
)

// Запись в agile-доски идёт через действие AgileBoards/SaveTicket.
// Особенности платформы, выясненные на живой системе:
//   - новая сущность создаётся с отрицательным Id: и у ссылки, и у самой карточки
//     нужен Id = -1, ноль означает «найди существующую» и даёт ошибку;
//   - теги и исполнители передаются списком ссылок со State = "Added";
//   - при обновлении карточка перезаписывается целиком, поэтому сначала
//     читаем текущую и меняем только затронутые поля.

const newID = -1

type ticketRef struct {
	ID       int64      `json:"Id"`
	Position int        `json:"Position"`
	ColumnID int64      `json:"ColumnId"`
	Ticket   ticketWire `json:"Ticket"`
}

type tagRef struct {
	ID    int64  `json:"Id"`
	State string `json:"State"`
	TagID int64  `json:"TicketTagId"`
}

type perfRef struct {
	ID     int64  `json:"Id"`
	State  string `json:"State"`
	PerfID int64  `json:"PerformerId"`
}

type ticketWire struct {
	ID                  int64     `json:"Id"`
	BoardID             int64     `json:"BoardId"`
	Name                string    `json:"Name"`
	Description         string    `json:"Description"`
	Priority            int       `json:"Priority"`
	Deadline            *string   `json:"Deadline,omitempty"`
	IsEnabled           bool      `json:"IsEnabled"`
	IsDescribed         bool      `json:"IsDescribed"`
	Votes               int       `json:"Votes"`
	CommentsCount       int       `json:"CommentsCount"`
	AttachmentsCount    int       `json:"AttachmentsCount"`
	ChildrenCount       int       `json:"ChildrenCount"`
	IdleDays            int       `json:"IdleDays"`
	ClosedChildrenCount int       `json:"ClosedChildrenCount"`
	EntityVersion       int       `json:"EntityVersion"`
	TicketsTags         []tagRef  `json:"TicketsTags,omitempty"`
	Performers          []perfRef `json:"Performers,omitempty"`
}

// TicketInput описывает карточку в человеческих терминах: доску и колонку
// можно называть по имени, исполнителей и теги тоже.
type TicketInput struct {
	Board       string // имя доски или её Id
	Column      string // имя колонки или её Id; пусто = первая колонка доски
	Name        string
	Description string
	Deadline    *time.Time
	Priority    int      // 1..10, 0 = по умолчанию 5
	Performers  []string // имена сотрудников или Id
	Tags        []string // имена тегов
}

// Notes собирает предупреждения, которые не являются ошибкой,
// но о которых пользователю надо сказать (не нашли тег, не нашли человека).
type Notes []string

func (n *Notes) add(f string, a ...any) { *n = append(*n, fmt.Sprintf(f, a...)) }

// ResolveBoard находит доску по Id или части имени.
func (s *Service) ResolveBoard(ctx context.Context, q string) (*Board, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, errors.New("не указана доска")
	}
	if id, err := strconv.ParseInt(q, 10, 64); err == nil {
		var b Board
		if err := s.c.Get(ctx, "IBoards", id, odata.Query{Select: "Id,Name,Prefix,Status"}, &b); err != nil {
			return nil, fmt.Errorf("доска #%d: %w", id, err)
		}
		return &b, nil
	}
	page, err := s.c.List(ctx, "IBoards", odata.Query{
		Filter: "Status eq 'Active' and (contains(Name," + odata.Quote(q) + ") or contains(Prefix," + odata.Quote(strings.ToUpper(q)) + "))",
		Select: "Id,Name,Prefix,Status", Top: 5,
	})
	if err != nil {
		return nil, err
	}
	bs, err := decodeList[Board](page)
	if err != nil {
		return nil, err
	}
	switch len(bs) {
	case 0:
		return nil, fmt.Errorf("доска %q не найдена, посмотрите список в rx_boards", q)
	case 1:
		return &bs[0], nil
	}
	var names []string
	for _, b := range bs {
		names = append(names, fmt.Sprintf("%s (#%d)", b.Name, b.ID))
	}
	return nil, fmt.Errorf("под %q подходит несколько досок: %s. Уточните название или укажите Id", q, strings.Join(names, ", "))
}

// resolveColumn находит колонку доски по Id или имени; пустой запрос даёт первую.
func (s *Service) resolveColumn(ctx context.Context, boardID int64, q string) (*Column, error) {
	page, err := s.c.List(ctx, "IColumns", odata.Query{
		Filter: fmt.Sprintf("BoardId eq %d and Status eq 'Active'", boardID),
		Select: "Id,Name,IsFinal,WipLimit,Status", Top: 50,
	})
	if err != nil {
		return nil, err
	}
	cols, err := decodeList[Column](page)
	if err != nil || len(cols) == 0 {
		return nil, fmt.Errorf("у доски #%d нет активных колонок", boardID)
	}
	if err := s.orderColumns(ctx, boardID, cols); err != nil {
		return nil, err
	}
	q = strings.TrimSpace(q)
	if q == "" {
		return &cols[0], nil
	}
	if id, e := strconv.ParseInt(q, 10, 64); e == nil {
		for i := range cols {
			if cols[i].ID == id {
				return &cols[i], nil
			}
		}
		return nil, fmt.Errorf("колонки #%d нет на доске #%d", id, boardID)
	}
	lq := strings.ToLower(q)
	for i := range cols {
		if strings.EqualFold(cols[i].Name, q) {
			return &cols[i], nil
		}
	}
	for i := range cols {
		if strings.Contains(strings.ToLower(cols[i].Name), lq) {
			return &cols[i], nil
		}
	}
	var names []string
	for _, c := range cols {
		names = append(names, c.Name)
	}
	return nil, fmt.Errorf("колонка %q не найдена. На доске есть: %s", q, strings.Join(names, ", "))
}

// orderColumns расставляет колонки в порядке доски (порядок хранится в самой доске).
func (s *Service) orderColumns(ctx context.Context, boardID int64, cols []Column) error {
	var bc struct {
		Columns []struct {
			IndexColumn int `json:"IndexColumn"`
			Column      Ref `json:"Column"`
		} `json:"Columns"`
	}
	if err := s.c.Get(ctx, "IBoards", boardID, odata.Query{Select: "Id", Expand: "Columns($select=IndexColumn;$expand=Column($select=Id))"}, &bc); err != nil {
		return nil // порядок не критичен
	}
	idx := map[int64]int{}
	for _, c := range bc.Columns {
		idx[c.Column.ID] = c.IndexColumn
	}
	for i := 0; i < len(cols); i++ {
		for j := i + 1; j < len(cols); j++ {
			if idx[cols[j].ID] < idx[cols[i].ID] {
				cols[i], cols[j] = cols[j], cols[i]
			}
		}
	}
	return nil
}

// resolveTags превращает имена тегов в идентификаторы. Создавать теги платформа
// через этот API не даёт, поэтому ненайденные возвращаются отдельным списком.
func (s *Service) resolveTags(ctx context.Context, names []string, notes *Notes) ([]int64, error) {
	var ids []int64
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if id, e := strconv.ParseInt(n, 10, 64); e == nil {
			ids = append(ids, id)
			continue
		}
		page, err := s.c.List(ctx, "INamedTags", odata.Query{
			Filter: "Status eq 'Active' and contains(Name," + odata.Quote(n) + ")", Select: "Id,Name", Top: 5,
		})
		if err != nil {
			return nil, err
		}
		tags, _ := decodeList[Ref](page)
		if len(tags) == 0 {
			notes.add("тег %q не найден, карточка создана без него: теги заводятся в интерфейсе доски", n)
			continue
		}
		best := tags[0]
		for _, t := range tags {
			if strings.EqualFold(t.Name, n) {
				best = t
				break
			}
		}
		ids = append(ids, best.ID)
	}
	return ids, nil
}

// resolvePerformers превращает имена сотрудников в идентификаторы.
func (s *Service) resolvePerformers(ctx context.Context, names []string, notes *Notes) ([]int64, error) {
	var ids []int64
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if id, e := strconv.ParseInt(n, 10, 64); e == nil {
			ids = append(ids, id)
			continue
		}
		emps, err := s.FindEmployees(ctx, n, false, 5)
		if err != nil {
			return nil, err
		}
		if len(emps) == 0 {
			notes.add("сотрудник %q не найден, карточка создана без него", n)
			continue
		}
		if len(emps) > 1 {
			var who []string
			for _, e := range emps {
				who = append(who, fmt.Sprintf("%s (#%d)", e.Name, e.ID))
			}
			return nil, fmt.Errorf("под %q подходит несколько сотрудников: %s. Уточните или укажите Id", n, strings.Join(who, ", "))
		}
		ids = append(ids, emps[0].ID)
	}
	return ids, nil
}

func refsFromTags(ids []int64) []tagRef {
	out := make([]tagRef, 0, len(ids))
	for _, id := range ids {
		out = append(out, tagRef{ID: newID, State: "Added", TagID: id})
	}
	return out
}

func refsFromPerformers(ids []int64) []perfRef {
	out := make([]perfRef, 0, len(ids))
	for _, id := range ids {
		out = append(out, perfRef{ID: newID, State: "Added", PerfID: id})
	}
	return out
}

type saveResult struct {
	IsSuccessed   bool   `json:"IsSuccessed"`
	ResultMessage string `json:"ResultMessage"`
	OriginalID    int64  `json:"OriginalTicketId"`
	TicketRef     struct {
		ID       int64  `json:"Id"`
		ColumnID int64  `json:"ColumnId"`
		Ticket   Ticket `json:"Ticket"`
	} `json:"TicketRef"`
}

func (s *Service) saveTicket(ctx context.Context, boardID int64, ref ticketRef) (*saveResult, error) {
	data, err := s.c.Action(ctx, "AgileBoards", "SaveTicket", map[string]any{
		"appId": "rxmcp", "boardId": boardID, "needLock": false, "ticketReference": ref,
	})
	if err != nil {
		return nil, err
	}
	var r saveResult
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("ответ доски не разобран: %w", err)
	}
	if !r.IsSuccessed {
		msg := r.ResultMessage
		if msg == "" {
			msg = "доска отклонила изменение без объяснения"
		}
		return nil, errors.New(msg)
	}
	return &r, nil
}

// CreateTicket заводит карточку на доске.
func (s *Service) CreateTicket(ctx context.Context, in TicketInput) (*Ticket, *Board, *Column, Notes, error) {
	var notes Notes
	if strings.TrimSpace(in.Name) == "" {
		return nil, nil, nil, notes, errors.New("нужно название карточки")
	}
	b, err := s.ResolveBoard(ctx, in.Board)
	if err != nil {
		return nil, nil, nil, notes, err
	}
	col, err := s.resolveColumn(ctx, b.ID, in.Column)
	if err != nil {
		return nil, b, nil, notes, err
	}
	tagIDs, err := s.resolveTags(ctx, in.Tags, &notes)
	if err != nil {
		return nil, b, col, notes, err
	}
	perfIDs, err := s.resolvePerformers(ctx, in.Performers, &notes)
	if err != nil {
		return nil, b, col, notes, err
	}
	pr := in.Priority
	if pr <= 0 {
		pr = 5
	}
	if pr > 10 {
		pr = 10
	}
	t := ticketWire{
		ID: newID, BoardID: b.ID, Name: in.Name, Description: in.Description,
		Priority: pr, IsEnabled: true, IsDescribed: in.Description != "",
		TicketsTags: refsFromTags(tagIDs), Performers: refsFromPerformers(perfIDs),
	}
	if in.Deadline != nil {
		d := in.Deadline.UTC().Format(time.RFC3339)
		t.Deadline = &d
	}
	r, err := s.saveTicket(ctx, b.ID, ticketRef{ID: newID, Position: 0, ColumnID: col.ID, Ticket: t})
	if err != nil {
		return nil, b, col, notes, err
	}
	out := r.TicketRef.Ticket
	if out.ID == 0 {
		out.ID = r.OriginalID
	}
	// SaveTicket отдаёт карточку без раскрытых ссылок: ни тегов, ни исполнителей,
	// ни автора. Перечитываем, иначе в ответе будет «не назначено» на только что
	// назначенного человека.
	return s.reread(ctx, out.ID, &out), b, col, notes, nil
}

// reread возвращает карточку, прочитанную обычным путём; если чтение не удалось,
// отдаёт то, что вернула запись, — терять подтверждение из-за чтения незачем.
func (s *Service) reread(ctx context.Context, id int64, fallback *Ticket) *Ticket {
	if id == 0 {
		return fallback
	}
	if t, err := s.TicketByID(ctx, id); err == nil {
		return t
	}
	return fallback
}

// ticketPlacement находит ссылку карточки на доске: её Id и колонку.
func (s *Service) ticketPlacement(ctx context.Context, boardID, ticketID int64) (refID int64, colID int64, err error) {
	page, err := s.c.List(ctx, "IColumns", odata.Query{
		Filter: fmt.Sprintf("BoardId eq %d and Status eq 'Active'", boardID),
		Select: "Id,Name", Expand: "Tickets($select=Id,Position;$expand=Ticket($select=Id))", Top: 50,
	})
	if err != nil {
		return 0, 0, err
	}
	cols, err := decodeList[Column](page)
	if err != nil {
		return 0, 0, err
	}
	for _, c := range cols {
		for _, t := range c.Tickets {
			if t.Ticket != nil && t.Ticket.ID == ticketID {
				return t.ID, c.ID, nil
			}
		}
	}
	return 0, 0, fmt.Errorf("карточка #%d не найдена на доске #%d", ticketID, boardID)
}

// UpdateTicket меняет поля существующей карточки. Пустые поля не трогаются.
func (s *Service) UpdateTicket(ctx context.Context, ticketID int64, in TicketInput, moveTo string) (*Ticket, Notes, error) {
	var notes Notes
	cur, err := s.TicketByID(ctx, ticketID)
	if err != nil {
		return nil, notes, err
	}
	refID, colID, err := s.ticketPlacement(ctx, cur.BoardID, ticketID)
	if err != nil {
		return nil, notes, err
	}
	if moveTo != "" {
		col, err := s.resolveColumn(ctx, cur.BoardID, moveTo)
		if err != nil {
			return nil, notes, err
		}
		colID = col.ID
	}
	t := ticketWire{
		ID: cur.ID, BoardID: cur.BoardID, Name: cur.Name, Description: cur.Description,
		Priority: 5, IsEnabled: true, IsDescribed: cur.Description != "",
		Votes: cur.Votes, CommentsCount: cur.Comments,
	}
	if cur.Priority != nil {
		t.Priority = *cur.Priority
	}
	if cur.Deadline != nil {
		d := cur.Deadline.UTC().Format(time.RFC3339)
		t.Deadline = &d
	}
	if in.Name != "" {
		t.Name = in.Name
	}
	if in.Description != "" {
		t.Description = in.Description
		t.IsDescribed = true
	}
	if in.Priority > 0 {
		t.Priority = min(in.Priority, 10)
	}
	if in.Deadline != nil {
		d := in.Deadline.UTC().Format(time.RFC3339)
		t.Deadline = &d
	}
	if len(in.Tags) > 0 {
		have := map[int64]bool{}
		for _, x := range cur.TicketsTags {
			if x.Tag != nil {
				have[x.Tag.ID] = true
			}
		}
		ids, err := s.resolveTags(ctx, in.Tags, &notes)
		if err != nil {
			return nil, notes, err
		}
		var add []int64
		for _, id := range ids {
			if !have[id] {
				add = append(add, id)
			}
		}
		t.TicketsTags = refsFromTags(add)
	}
	if len(in.Performers) > 0 {
		have := map[int64]bool{}
		for _, x := range cur.Performers {
			if x.Performer != nil {
				have[x.Performer.ID] = true
			}
		}
		ids, err := s.resolvePerformers(ctx, in.Performers, &notes)
		if err != nil {
			return nil, notes, err
		}
		var add []int64
		for _, id := range ids {
			if !have[id] {
				add = append(add, id)
			}
		}
		t.Performers = refsFromPerformers(add)
	}
	r, err := s.saveTicket(ctx, cur.BoardID, ticketRef{ID: refID, Position: 0, ColumnID: colID, Ticket: t})
	if err != nil {
		return nil, notes, err
	}
	out := r.TicketRef.Ticket
	if out.ID == 0 {
		out.ID = ticketID
	}
	return s.reread(ctx, out.ID, &out), notes, nil
}
