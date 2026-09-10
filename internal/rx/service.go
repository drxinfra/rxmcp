// Package rx: доменные запросы к Directum RX поверх OData и их форматирование.
package rx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/drxinfra/rxmcp/internal/odata"
)

// Service выполняет запросы от имени одного пользователя RX.
type Service struct {
	c        *odata.Client
	login    string
	userID   int64
	pageSize int
	maxPage  int
	F        Formatter

	mu   sync.Mutex
	me   *Me
	meAt time.Time
}

// Me описание текущего пользователя.
type Me struct {
	ID         int64
	Name       string
	LoginName  string
	LoginType  string
	Department string
	JobTitle   string
	Email      string
}

// New создаёт сервис. userID можно оставить 0, тогда он вычисляется по логину.
func New(c *odata.Client, login string, userID int64, pageSize, maxPage int, f Formatter) *Service {
	if pageSize <= 0 {
		pageSize = 20
	}
	if maxPage <= 0 {
		maxPage = 100
	}
	return &Service{c: c, login: login, userID: userID, pageSize: pageSize, maxPage: maxPage, F: f}
}

func (s *Service) limit(n int) int {
	if n <= 0 {
		return s.pageSize
	}
	if n > s.maxPage {
		return s.maxPage
	}
	return n
}

// WhoAmI возвращает текущего пользователя, с кэшем на время процесса.
func (s *Service) WhoAmI(ctx context.Context) (*Me, error) {
	s.mu.Lock()
	if s.me != nil {
		m := *s.me
		s.mu.Unlock()
		return &m, nil
	}
	s.mu.Unlock()

	var u *User
	if s.userID != 0 {
		var got User
		if err := s.c.Get(ctx, "IUsers", s.userID, odata.Query{Select: "Id,Name,Status", Expand: "Login($select=LoginName,TypeAuthentication)"}, &got); err != nil {
			return nil, fmt.Errorf("пользователь RXMCP_USER_ID=%d: %w", s.userID, err)
		}
		u = &got
	} else {
		if s.login == "" {
			return nil, errors.New("не могу определить пользователя: задайте RXMCP_LOGIN или RXMCP_USER_ID")
		}
		page, err := s.c.List(ctx, "IUsers", odata.Query{
			Filter: "Login/LoginName eq " + odata.Quote(s.login),
			Select: "Id,Name,Status",
			Expand: "Login($select=LoginName,TypeAuthentication)",
			Top:    2,
		})
		if err != nil {
			return nil, err
		}
		if len(page.Value) == 0 {
			// Логин мог быть с доменом или в другом регистре: пробуем по хвосту.
			short := s.login
			if i := strings.LastIndexAny(short, `\@`); i >= 0 && i < len(short)-1 {
				short = short[i+1:]
			}
			page, err = s.c.List(ctx, "IUsers", odata.Query{
				Filter: "contains(tolower(Login/LoginName)," + odata.Quote(strings.ToLower(short)) + ")",
				Select: "Id,Name,Status",
				Expand: "Login($select=LoginName,TypeAuthentication)",
				Top:    5,
			})
			if err != nil {
				return nil, err
			}
		}
		if len(page.Value) == 0 {
			return nil, fmt.Errorf("пользователь с логином %q в RX не найден; задайте RXMCP_USER_ID", s.login)
		}
		var got User
		if err := json.Unmarshal(page.Value[0], &got); err != nil {
			return nil, err
		}
		u = &got
	}
	m := &Me{ID: u.ID, Name: u.Name}
	if u.Login != nil {
		m.LoginName = u.Login.LoginName
		m.LoginType = u.Login.TypeAuthentication
	}
	var e Employee
	if err := s.c.Get(ctx, "IEmployees", u.ID, odata.Query{
		Select: "Id,Name,Email",
		Expand: "Department($select=Id,Name),JobTitle($select=Id,Name)",
	}, &e); err == nil {
		m.Email = e.Email
		if e.Department != nil {
			m.Department = e.Department.Name
		}
		if e.JobTitle != nil {
			m.JobTitle = e.JobTitle.Name
		}
	}
	s.mu.Lock()
	s.me = m
	s.meAt = time.Now()
	s.mu.Unlock()
	out := *m
	return &out, nil
}

// MeText текст «кто я».
func (m *Me) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Вы в RX: %s (id %d)", m.Name, m.ID)
	if m.JobTitle != "" {
		fmt.Fprintf(&b, ", %s", m.JobTitle)
	}
	if m.Department != "" {
		fmt.Fprintf(&b, ", %s", m.Department)
	}
	b.WriteString("\n")
	if m.LoginName != "" {
		fmt.Fprintf(&b, "Логин: %s", m.LoginName)
		if m.LoginType != "" {
			fmt.Fprintf(&b, " (тип входа %s)", m.LoginType)
		}
		b.WriteString("\n")
	}
	if m.Email != "" {
		fmt.Fprintf(&b, "Почта: %s\n", m.Email)
	}
	return strings.TrimRight(b.String(), "\n")
}

// AssignmentFilter параметры списка заданий.
type AssignmentFilter struct {
	// in_process | overdue | unread | completed | all
	Status  string
	Notices bool
	Subject string
	Limit   int
}

// MyAssignments список моих заданий.
func (s *Service) MyAssignments(ctx context.Context, fl AssignmentFilter) ([]Assignment, *int64, error) {
	me, err := s.WhoAmI(ctx)
	if err != nil {
		return nil, nil, err
	}
	set := "IAssignments"
	if fl.Notices {
		set = "INotices"
	}
	parts := []string{fmt.Sprintf("Performer/Id eq %d", me.ID)}
	order := "Deadline asc,Created desc"
	now := time.Now().UTC().Format(time.RFC3339)
	switch strings.ToLower(strings.TrimSpace(fl.Status)) {
	case "", "in_process", "inprocess", "active":
		parts = append(parts, "Status eq 'InProcess'")
	case "overdue":
		parts = append(parts, "Status eq 'InProcess'", "Deadline lt "+now)
	case "unread":
		parts = append(parts, "Status eq 'InProcess'", "IsRead eq false")
	case "completed":
		parts = append(parts, "Status eq 'Completed'")
		order = "Modified desc"
	case "all":
		order = "Created desc"
	default:
		return nil, nil, fmt.Errorf("status: допустимо in_process, overdue, unread, completed, all")
	}
	if fl.Subject != "" {
		parts = append(parts, "contains(Subject,"+odata.Quote(fl.Subject)+")")
	}
	sel := "Id,Subject,Status,Importance,Deadline,Created,Modified,IsRead"
	if !fl.Notices {
		sel += ",Completed,Result"
	}
	page, err := s.c.List(ctx, set, odata.Query{
		Filter:  strings.Join(parts, " and "),
		Select:  sel,
		Expand:  "Author($select=Id,Name),Task($select=Id,Subject,Status)",
		OrderBy: order,
		Top:     s.limit(fl.Limit),
		Count:   true,
	})
	if err != nil {
		return nil, nil, err
	}
	out := make([]Assignment, 0, len(page.Value))
	for _, raw := range page.Value {
		var a Assignment
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, nil, err
		}
		out = append(out, a)
	}
	return out, page.Count, nil
}

// AssignmentsText форматирует список.
func (s *Service) AssignmentsText(items []Assignment, total *int64, what string) string {
	if len(items) == 0 {
		return "Нет " + what + " по этим условиям."
	}
	var b strings.Builder
	if total != nil && int(*total) > len(items) {
		fmt.Fprintf(&b, "%s: показано %d из %d (уточните условия или увеличьте limit)\n", strings.ToUpper(what[:2])+what[2:], len(items), *total)
	} else {
		fmt.Fprintf(&b, "%s: %d\n", strings.ToUpper(what[:2])+what[2:], len(items))
	}
	for _, a := range items {
		b.WriteString(s.F.AssignmentLine(a))
		b.WriteString("\n")
	}
	b.WriteString("● = не прочитано. Подробности: rx_get_assignment.")
	return b.String()
}

// GetAssignment задание с перепиской и вложениями.
func (s *Service) GetAssignment(ctx context.Context, id int64) (*Assignment, []Ref, error) {
	var a Assignment
	err := s.c.Get(ctx, "IAssignments", id, odata.Query{
		Expand: "Performer($select=Id,Name),Author($select=Id,Name),Task($select=Id,Subject,Status),Texts($expand=Author($select=Id,Name)),AttachmentDetails",
	}, &a)
	if err != nil {
		// Уведомление лежит в другом наборе.
		var n Assignment
		if err2 := s.c.Get(ctx, "INotices", id, odata.Query{
			Expand: "Performer($select=Id,Name),Author($select=Id,Name),Task($select=Id,Subject,Status),Texts($expand=Author($select=Id,Name)),AttachmentDetails",
		}, &n); err2 != nil {
			return nil, nil, err
		}
		a = n
	}
	sortTexts(a.Texts)
	att, _ := s.attachmentNames(ctx, a.Attachments)
	return &a, att, nil
}

// GetTask задача с перепиской, вложениями и заданиями.
func (s *Service) GetTask(ctx context.Context, id int64) (*Task, []Ref, []Assignment, error) {
	var t Task
	if err := s.c.Get(ctx, "ITasks", id, odata.Query{
		Expand: "Author($select=Id,Name),StartedBy($select=Id,Name),Texts($expand=Author($select=Id,Name)),AttachmentDetails",
	}, &t); err != nil {
		return nil, nil, nil, err
	}
	sortTexts(t.Texts)
	att, _ := s.attachmentNames(ctx, t.Attachments)
	var jobs []Assignment
	if page, err := s.c.List(ctx, "IAssignments", odata.Query{
		Filter:  fmt.Sprintf("Task/Id eq %d", id),
		Select:  "Id,Subject,Status,Deadline,Completed,Result,Created",
		Expand:  "Performer($select=Id,Name)",
		OrderBy: "Created asc",
		Top:     s.maxPage,
	}); err == nil {
		for _, raw := range page.Value {
			var a Assignment
			if json.Unmarshal(raw, &a) == nil {
				jobs = append(jobs, a)
			}
		}
	}
	return &t, att, jobs, nil
}

// TaskFilter параметры списка задач.
type TaskFilter struct {
	// mine (автор я) | all
	Who     string
	Status  string
	Subject string
	Limit   int
}

// ListTasks задачи.
func (s *Service) ListTasks(ctx context.Context, fl TaskFilter) ([]Task, *int64, error) {
	var parts []string
	if strings.ToLower(fl.Who) != "all" {
		me, err := s.WhoAmI(ctx)
		if err != nil {
			return nil, nil, err
		}
		parts = append(parts, fmt.Sprintf("Author/Id eq %d", me.ID))
	}
	switch strings.ToLower(strings.TrimSpace(fl.Status)) {
	case "", "in_process", "inprocess", "active":
		parts = append(parts, "Status eq 'InProcess'")
	case "completed":
		parts = append(parts, "Status eq 'Completed'")
	case "aborted":
		parts = append(parts, "Status eq 'Aborted'")
	case "draft":
		parts = append(parts, "Status eq 'Draft'")
	case "all":
	default:
		return nil, nil, fmt.Errorf("status: допустимо in_process, completed, aborted, draft, all")
	}
	if fl.Subject != "" {
		parts = append(parts, "contains(Subject,"+odata.Quote(fl.Subject)+")")
	}
	page, err := s.c.List(ctx, "ITasks", odata.Query{
		Filter:  strings.Join(parts, " and "),
		Select:  "Id,Subject,Status,Importance,Created,Started,MaxDeadline",
		Expand:  "Author($select=Id,Name)",
		OrderBy: "Created desc",
		Top:     s.limit(fl.Limit),
		Count:   true,
	})
	if err != nil {
		return nil, nil, err
	}
	out := make([]Task, 0, len(page.Value))
	for _, raw := range page.Value {
		var t Task
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, nil, err
		}
		out = append(out, t)
	}
	return out, page.Count, nil
}

// DocumentFilter параметры поиска документов.
type DocumentFilter struct {
	Query       string
	Kind        string
	Author      string
	RegNumber   string
	CreatedFrom string // YYYY-MM-DD
	CreatedTo   string
	State       string // active | draft | obsolete | ""
	AllTypes    bool
	Limit       int
}

// FindDocuments поиск документов.
func (s *Service) FindDocuments(ctx context.Context, fl DocumentFilter) ([]Document, *int64, error) {
	set := "IOfficialDocuments"
	sel := "Id,Name,Created,Modified,HasVersions,RegistrationNumber,RegistrationDate,DocumentDate,LifeCycleState,Subject"
	if fl.AllTypes {
		set = "IElectronicDocuments"
		sel = "Id,Name,Created,Modified,HasVersions"
	}
	var parts []string
	if q := strings.TrimSpace(fl.Query); q != "" {
		parts = append(parts, "contains(Name,"+odata.Quote(q)+")")
	}
	if fl.Kind != "" {
		if fl.AllTypes {
			return nil, nil, errors.New("kind работает только среди официальных документов (all_types=false)")
		}
		parts = append(parts, "contains(DocumentKind/Name,"+odata.Quote(fl.Kind)+")")
	}
	if fl.Author != "" {
		parts = append(parts, "contains(Author/Name,"+odata.Quote(fl.Author)+")")
	}
	if fl.RegNumber != "" && !fl.AllTypes {
		parts = append(parts, "RegistrationNumber eq "+odata.Quote(fl.RegNumber))
	}
	if fl.CreatedFrom != "" {
		d, err := parseDay(fl.CreatedFrom)
		if err != nil {
			return nil, nil, fmt.Errorf("created_from: %w", err)
		}
		parts = append(parts, "Created ge "+d.Format(time.RFC3339))
	}
	if fl.CreatedTo != "" {
		d, err := parseDay(fl.CreatedTo)
		if err != nil {
			return nil, nil, fmt.Errorf("created_to: %w", err)
		}
		parts = append(parts, "Created lt "+d.Add(24*time.Hour).Format(time.RFC3339))
	}
	if fl.State != "" && !fl.AllTypes {
		st := map[string]string{"active": "Active", "draft": "Draft", "obsolete": "Obsolete"}[strings.ToLower(fl.State)]
		if st == "" {
			return nil, nil, errors.New("state: допустимо active, draft, obsolete")
		}
		parts = append(parts, "LifeCycleState eq '"+st+"'")
	}
	if len(parts) == 0 {
		return nil, nil, errors.New("задайте хотя бы одно условие: query, kind, author, registration_number или даты")
	}
	expand := "Author($select=Id,Name),DocumentKind($select=Id,Name)"
	if fl.AllTypes {
		expand = "Author($select=Id,Name)"
	}
	page, err := s.c.List(ctx, set, odata.Query{
		Filter:  strings.Join(parts, " and "),
		Select:  sel,
		Expand:  expand,
		OrderBy: "Modified desc",
		Top:     s.limit(fl.Limit),
		Count:   true,
	})
	if err != nil {
		return nil, nil, err
	}
	out := make([]Document, 0, len(page.Value))
	for _, raw := range page.Value {
		var d Document
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, nil, err
		}
		out = append(out, d)
	}
	return out, page.Count, nil
}

// GetDocument карточка документа с версиями.
func (s *Service) GetDocument(ctx context.Context, id int64) (*Document, error) {
	var d Document
	if err := s.c.Get(ctx, "IElectronicDocuments", id, odata.Query{
		Expand: "Author($select=Id,Name),Versions($select=Id,Number,Note,Created,IsHidden;$expand=AssociatedApplication($select=Name,Extension))",
	}, &d); err != nil {
		return nil, err
	}
	// Поля официального документа (вид, регистрация, состояния), если это он.
	var o Document
	if err := s.c.Get(ctx, "IOfficialDocuments", id, odata.Query{
		Select: "Id,Subject,Note,RegistrationNumber,RegistrationDate,DocumentDate,LifeCycleState,RegistrationState,InternalApprovalState,ExternalApprovalState,ExecutionState",
		Expand: "DocumentKind($select=Id,Name),Department($select=Id,Name),BusinessUnit($select=Id,Name),OurSignatory($select=Id,Name),Assignee($select=Id,Name)",
	}, &o); err == nil {
		d.DocumentKind = o.DocumentKind
		d.Subject, d.Note = o.Subject, o.Note
		d.RegistrationNumber, d.RegistrationDate, d.DocumentDate = o.RegistrationNumber, o.RegistrationDate, o.DocumentDate
		d.LifeCycleState, d.RegistrationState = o.LifeCycleState, o.RegistrationState
		d.InternalApproval, d.ExternalApproval, d.ExecutionState = o.InternalApproval, o.ExternalApproval, o.ExecutionState
		d.Department, d.BusinessUnit, d.OurSignatory, d.Assignee = o.Department, o.BusinessUnit, o.OurSignatory, o.Assignee
	}
	sort.Slice(d.Versions, func(i, j int) bool { return d.Versions[i].Number < d.Versions[j].Number })
	return &d, nil
}

// VersionBody скачивает тело версии. versionID = 0 означает последнюю.
func (s *Service) VersionBody(ctx context.Context, doc *Document, versionID int64) (data []byte, ext string, v *Version, err error) {
	if len(doc.Versions) == 0 {
		return nil, "", nil, errors.New("у документа нет версий")
	}
	for i := range doc.Versions {
		if versionID == 0 && !doc.Versions[i].IsHidden {
			v = &doc.Versions[i]
		}
		if versionID != 0 && doc.Versions[i].ID == versionID {
			v = &doc.Versions[i]
		}
	}
	if v == nil && versionID == 0 {
		v = &doc.Versions[len(doc.Versions)-1]
	}
	if v == nil {
		return nil, "", nil, fmt.Errorf("версии с id %d у документа нет", versionID)
	}
	if v.App != nil {
		ext = strings.ToLower(strings.TrimPrefix(v.App.Extension, "."))
	}
	path := fmt.Sprintf("IElectronicDocuments(%d)/Versions(%d)/Body/$value", doc.ID, v.ID)
	data, _, err = s.c.Raw(ctx, path)
	if err != nil {
		return nil, ext, v, err
	}
	return data, ext, v, nil
}

// FindEmployees сотрудники по подстроке имени.
func (s *Service) FindEmployees(ctx context.Context, q string, includeInactive bool, limit int) ([]Employee, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, errors.New("задайте фамилию или часть имени")
	}
	filter := "contains(Name," + odata.Quote(q) + ")"
	if !includeInactive {
		filter += " and Status eq 'Active'"
	}
	page, err := s.c.List(ctx, "IEmployees", odata.Query{
		Filter:  filter,
		Select:  "Id,Name,Status,Email,Phone",
		Expand:  "Department($select=Id,Name),JobTitle($select=Id,Name)",
		OrderBy: "Name asc",
		Top:     s.limit(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Employee, 0, len(page.Value))
	for _, raw := range page.Value {
		var e Employee
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// --- запись ---

// CompleteAssignment выполняет задание.
func (s *Service) CompleteAssignment(ctx context.Context, id int64, result string) error {
	params := map[string]any{"assignmentId": id}
	if result != "" {
		params["result"] = result
	}
	_, err := s.c.Action(ctx, "Docflow", "CompleteAssignment", params)
	return err
}

// SimpleTaskInput параметры простой задачи.
type SimpleTaskInput struct {
	Subject      string
	Text         string
	PerformerIDs []int64
	ObserverIDs  []int64
	DocumentIDs  []int64
	Deadline     *time.Time
	Importance   string // Low | Normal | High
	Notice       bool   // уведомление вместо задания
	Start        bool
}

// CreateSimpleTask создаёт простую задачу и при Start стартует её. Возвращает Id задачи.
func (s *Service) CreateSimpleTask(ctx context.Context, in SimpleTaskInput) (int64, error) {
	if strings.TrimSpace(in.Subject) == "" {
		return 0, errors.New("нужна тема задачи")
	}
	if len(in.PerformerIDs) == 0 {
		return 0, errors.New("нужен хотя бы один исполнитель (Id сотрудника, см. rx_find_employees)")
	}
	at := "Assignment"
	if in.Notice {
		at = "Notice"
	}
	imp := "Normal"
	switch strings.ToLower(in.Importance) {
	case "low":
		imp = "Low"
	case "high":
		imp = "High"
	}
	params := map[string]any{
		"assignmentType": at,
		"subject":        in.Subject,
		"importance":     imp,
		"text":           in.Text,
		"performerIds":   in.PerformerIDs,
		"observerIds":    nz(in.ObserverIDs),
		"documentIds":    nz(in.DocumentIDs),
	}
	if in.Deadline != nil {
		params["deadline"] = in.Deadline.UTC().Format(time.RFC3339)
	}
	data, err := s.c.Action(ctx, "Docflow", "CreateSimpleTask", params)
	if err != nil {
		return 0, err
	}
	id, err := actionInt(data)
	if err != nil {
		return 0, fmt.Errorf("задача создана, но Id не разобран: %w", err)
	}
	if in.Start {
		if _, err := s.c.Action(ctx, "Docflow", "StartTask", map[string]any{"taskId": id}); err != nil {
			return id, fmt.Errorf("задача #%d создана черновиком, но не стартована: %w", id, err)
		}
	}
	return id, nil
}

// AbortTask прекращает задачу.
func (s *Service) AbortTask(ctx context.Context, id int64) error {
	_, err := s.c.Action(ctx, "Shell", "AbortTask", map[string]any{"taskId": id})
	return err
}

// --- вспомогательное ---

func nz(v []int64) []int64 {
	if v == nil {
		return []int64{}
	}
	return v
}

func actionInt(data []byte) (int64, error) {
	var wrapped struct {
		Value json.Number `json:"value"`
	}
	if json.Unmarshal(data, &wrapped) == nil && wrapped.Value != "" {
		return wrapped.Value.Int64()
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return 0, err
	}
	return n.Int64()
}

func sortTexts(t []Text) {
	sort.SliceStable(t, func(i, j int) bool {
		if t[i].Created == nil || t[j].Created == nil {
			return false
		}
		return t[i].Created.Before(*t[j].Created)
	})
}

func parseDay(s string) (time.Time, error) {
	for _, l := range []string{"2006-01-02", "02.01.2006"} {
		if t, err := time.Parse(l, strings.TrimSpace(s)); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("дата %q: ожидается ГГГГ-ММ-ДД", s)
}

// attachmentNames подтягивает имена вложенных документов одним запросом.
func (s *Service) attachmentNames(ctx context.Context, att []Attachment) ([]Ref, error) {
	if len(att) == 0 {
		return nil, nil
	}
	seen := map[int64]bool{}
	var parts []string
	for _, a := range att {
		if a.AttachmentID == 0 || seen[a.AttachmentID] {
			continue
		}
		seen[a.AttachmentID] = true
		parts = append(parts, fmt.Sprintf("Id eq %d", a.AttachmentID))
		if len(parts) >= 30 {
			break
		}
	}
	page, err := s.c.List(ctx, "IElectronicDocuments", odata.Query{
		Filter: strings.Join(parts, " or "),
		Select: "Id,Name",
		Top:    30,
	})
	if err != nil {
		// Вложения могут быть не документами (папки, записи справочников): покажем только Id.
		out := make([]Ref, 0, len(seen))
		for id := range seen {
			out = append(out, Ref{ID: id, Name: "(не документ или нет доступа)"})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out, nil
	}
	found := map[int64]string{}
	for _, raw := range page.Value {
		var r Ref
		if json.Unmarshal(raw, &r) == nil {
			found[r.ID] = r.Name
		}
	}
	out := make([]Ref, 0, len(seen))
	for id := range seen {
		n, ok := found[id]
		if !ok {
			n = "(не документ или нет доступа)"
		}
		out = append(out, Ref{ID: id, Name: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
