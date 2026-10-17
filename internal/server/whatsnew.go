package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/drxinfra/rxmcp/internal/rx"
)

type whatsNewIn struct {
	Since string `json:"since,omitempty" jsonschema:"с какого момента: 24h, 3d, 1w, today, дата 2026-10-15 или 15.10.2026; по умолчанию с прошлого вызова rx_whats_new, а при первом вызове за сутки"`
	Peek  bool   `json:"peek,omitempty" jsonschema:"true = только посмотреть, не сдвигая отметку прошлого просмотра"`
	Limit int    `json:"limit,omitempty" jsonschema:"сколько строк в каждом разделе, по умолчанию 20, максимум 100"`
}

// Отметка прошлого просмотра: одна на стенд и пользователя, в каталоге настроек.
// Это не данные RX, а только время, поэтому принцип «данные пользователя не храним»
// она не нарушает.
type seenMark struct {
	Seen time.Time `json:"seen"`
}

func (s *Server) markFile() string {
	if s.opt.StateDir == "" {
		return ""
	}
	key := regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(s.opt.StateKey, "_")
	return filepath.Join(s.opt.StateDir, "whatsnew-"+key+".json")
}

func (s *Server) loadMark() (time.Time, bool) {
	f := s.markFile()
	if f == "" {
		return time.Time{}, false
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return time.Time{}, false
	}
	var m seenMark
	if json.Unmarshal(b, &m) != nil || m.Seen.IsZero() {
		return time.Time{}, false
	}
	return m.Seen, true
}

func (s *Server) saveMark(t time.Time) {
	f := s.markFile()
	if f == "" {
		return
	}
	b, _ := json.Marshal(seenMark{Seen: t})
	_ = os.MkdirAll(filepath.Dir(f), 0o700)
	_ = os.WriteFile(f, b, 0o600)
}

var reAgo = regexp.MustCompile(`^(\d+)\s*(h|ч|d|д|w|н)$`)

// parseSince разбирает since относительно now в часовом поясе loc.
func parseSince(v string, now time.Time, loc *time.Location) (time.Time, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "today" || v == "сегодня" {
		y, m, d := now.In(loc).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, loc), nil
	}
	if m := reAgo.FindStringSubmatch(v); m != nil {
		n, _ := strconv.Atoi(m[1])
		unit := map[string]time.Duration{"h": time.Hour, "ч": time.Hour, "d": 24 * time.Hour, "д": 24 * time.Hour, "w": 7 * 24 * time.Hour, "н": 7 * 24 * time.Hour}[m[2]]
		return now.Add(-time.Duration(n) * unit), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02", "02.01.2006 15:04", "02.01.2006"} {
		if t, err := time.ParseInLocation(layout, v, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("since %q не понят: подойдёт 24h, 3d, 1w, today или дата 2026-10-15", v)
}

func (s *Server) registerWhatsNew() {
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_whats_new",
		Description: "Что нового в Directum RX с прошлого раза: пришедшие задания и уведомления, что со сроком до конца дня (включая просроченное), какие мои задачи завершились. Используйте для вопросов «что нового», «что пришло, пока меня не было», в начале рабочего дня. По умолчанию показывает всё с прошлого вызова rx_whats_new и сдвигает отметку на текущий момент; since задаёт начало явно, peek=true не сдвигает отметку. Подробности по строке: rx_get_assignment, rx_get_task. Только чтение RX.",
		Annotations: ro("Что нового"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in whatsNewIn) (*mcp.CallToolResult, any, error) {
		now := time.Now()
		loc := s.svc.F.Loc
		if loc == nil {
			loc = time.Local
		}
		var since time.Time
		why := ""
		switch {
		case in.Since != "":
			t, err := parseSince(in.Since, now, loc)
			if err != nil {
				return fail(err)
			}
			since, why = t, "по запросу"
		default:
			if t, ok := s.loadMark(); ok {
				since, why = t, "с прошлого просмотра"
			} else {
				since, why = now.Add(-24*time.Hour), "за сутки: прошлого просмотра нет"
			}
		}
		y, m, d := now.In(loc).Date()
		endOfDay := time.Date(y, m, d+1, 0, 0, 0, 0, loc)

		var b strings.Builder
		fmt.Fprintf(&b, "Что нового в RX с %s (%s).\n\n", s.svc.F.DateTime(&since), why)

		items, total, err := s.svc.MyAssignments(ctx, rx.AssignmentFilter{Status: "all", CreatedSince: &since, Limit: in.Limit})
		if err != nil {
			return fail(err)
		}
		b.WriteString(section(s.svc, items, total, "новых заданий", "Новых заданий нет."))
		notes, ntotal, err := s.svc.MyAssignments(ctx, rx.AssignmentFilter{Status: "all", Notices: true, CreatedSince: &since, Limit: in.Limit})
		if err == nil {
			b.WriteString("\n\n" + section(s.svc, notes, ntotal, "новых уведомлений", "Новых уведомлений нет."))
		}
		due, dtotal, err := s.svc.MyAssignments(ctx, rx.AssignmentFilter{Status: "in_process", DueBefore: &endOfDay, Limit: in.Limit})
		if err == nil {
			b.WriteString("\n\n" + section(s.svc, due, dtotal, "заданий со сроком до конца дня, включая просроченные", "Заданий со сроком сегодня и просроченных нет."))
		}
		tasks, ttotal, err := s.svc.ListTasks(ctx, rx.TaskFilter{Status: "completed", ModifiedSince: &since, Limit: in.Limit})
		if err == nil {
			b.WriteString("\n\n")
			if len(tasks) == 0 {
				b.WriteString("Завершённых моих задач нет.")
			} else {
				n := len(tasks)
				if ttotal != nil {
					n = int(*ttotal)
				}
				fmt.Fprintf(&b, "Мои задачи, завершённые за это время: %d\n", n)
				for _, t := range tasks {
					b.WriteString(s.svc.F.TaskLine(t) + "\n")
				}
				b.WriteString("Подробности: rx_get_task.")
			}
		}
		b.WriteString("\n\n")
		if in.Peek {
			b.WriteString("Отметка прошлого просмотра не сдвинута (peek).")
		} else {
			s.saveMark(now)
			b.WriteString("Отметка прошлого просмотра сдвинута на сейчас: следующий вызов покажет то, что придёт после.")
		}
		return text(b.String()), nil, nil
	})
}

func section(svc *rx.Service, items []rx.Assignment, total *int64, what, empty string) string {
	if len(items) == 0 {
		return empty
	}
	out := svc.AssignmentsText(items, total, what)
	// Подсказку про ● и rx_get_assignment AssignmentsText пишет в конце каждого списка;
	// в сводке из нескольких списков хватит одной, она есть в описании инструмента.
	return strings.TrimSuffix(out, "\n● = не прочитано. Подробности: rx_get_assignment.")
}
