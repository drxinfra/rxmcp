package rx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/drxinfra/rxmcp/internal/odata"
)

// ErrSubstitutionsUnavailable справочник «Замещения» не читается через сервис интеграции:
// набора ISubstitutions нет в этой версии или конфигурации RX, на него нет прав, или его поля
// не те, что ожидаются. Вызывающие не падают, а показывают свои задания и говорят об этом.
var ErrSubstitutionsUnavailable = errors.New("справочник «Замещения» недоступен в сервисе интеграции")

const (
	// substitutionsTTL сколько помнить список замещений: он меняется редко, а нужен
	// каждому rx_whoami и спискам заданий с include_substituted.
	substitutionsTTL = 5 * time.Minute
	// Системных замещений у руководителя столько, сколько людей в подразделении,
	// поэтому справочник читается страницами, но не больше substitutionsMax записей.
	substitutionsPage = 200
	substitutionsMax  = 1000
)

// Substitution действующее замещение: текущий пользователь замещает User.
type Substitution struct {
	ID    int64
	User  Ref        // кого замещаем: отсутствующий пользователь
	Start *time.Time // с какого дня; nil = без даты начала
	End   *time.Time // по какой день включительно; nil = бессрочно
}

// substitutionDTO запись ISubstitutions. Даты читаются строкой и разбираются терпимо,
// $select не задаётся: незнакомое серверу поле в $select дало бы 400 на весь запрос.
type substitutionDTO struct {
	ID        int64  `json:"Id"`
	Status    string `json:"Status"`
	IsSystem  *bool  `json:"IsSystem"`
	StartDate string `json:"StartDate"`
	EndDate   string `json:"EndDate"`
	User      *Ref   `json:"User"`
}

// Substitutions кого текущий пользователь замещает сейчас: действующие записи справочника
// «Замещения», где он заместитель. Системные замещения (руководитель подразделения
// замещает всех своих сотрудников) не считаются: их папку «Входящие» RX замещающему
// не показывает, а руководителю они принесли бы задания всего подразделения.
// Даты и IsSystem проверяются здесь, а не в $filter: сравнения с null в OData сервис
// интеграции переводит в SQL по-разному. Ответ помнится substitutionsTTL; если справочник
// недоступен, ошибка удовлетворяет errors.Is(err, ErrSubstitutionsUnavailable).
func (s *Service) Substitutions(ctx context.Context) ([]Substitution, error) {
	s.mu.Lock()
	if !s.subsAt.IsZero() && time.Since(s.subsAt) < substitutionsTTL {
		subs, err := slices.Clone(s.subs), s.subsErr
		s.mu.Unlock()
		return subs, err
	}
	s.mu.Unlock()
	subs, err := s.loadSubstitutions(ctx)
	if err != nil && !errors.Is(err, ErrSubstitutionsUnavailable) {
		return nil, err // сбой связи или входа не запоминаем: следующий вызов спросит снова
	}
	s.mu.Lock()
	s.subs, s.subsErr, s.subsAt = subs, err, time.Now()
	s.mu.Unlock()
	return slices.Clone(subs), err
}

func (s *Service) loadSubstitutions(ctx context.Context) ([]Substitution, error) {
	me, err := s.WhoAmI(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if s.F.Now != nil {
		now = s.F.Now()
	}
	byUser := map[int64]Substitution{}
	for skip := 0; skip < substitutionsMax; {
		page, err := s.c.List(ctx, "ISubstitutions", odata.Query{
			Filter:  fmt.Sprintf("Substitute/Id eq %d and Status eq 'Active'", me.ID),
			Expand:  "User($select=Id,Name)",
			OrderBy: "Id",
			Top:     substitutionsPage,
			Skip:    skip,
			Count:   true,
		})
		if err != nil {
			var oe *odata.Error
			if errors.As(err, &oe) && (oe.Status == 400 || oe.Status == 403 || oe.Status == 404) {
				return nil, fmt.Errorf("%w: %v", ErrSubstitutionsUnavailable, err)
			}
			return nil, err
		}
		for _, raw := range page.Value {
			var d substitutionDTO
			if err := json.Unmarshal(raw, &d); err != nil {
				return nil, fmt.Errorf("%w: запись не разобрана: %v", ErrSubstitutionsUnavailable, err)
			}
			if d.User == nil || d.User.ID == 0 || d.User.ID == me.ID {
				continue
			}
			if (d.IsSystem != nil && *d.IsSystem) || (d.Status != "" && d.Status != "Active") {
				continue
			}
			sb := Substitution{ID: d.ID, User: *d.User, Start: parsePlanDate(d.StartDate), End: parsePlanDate(d.EndDate)}
			if !sb.activeAt(now) {
				continue
			}
			// Несколько записей на одного человека (продлили отпуск новой записью): берём ту,
			// что действует дольше.
			if prev, ok := byUser[sb.User.ID]; ok && !endsLater(sb.End, prev.End) {
				continue
			}
			byUser[sb.User.ID] = sb
		}
		// Сервер может отдать меньше, чем просили: дальше читаем, пока есть по счётчику
		// или по ссылке на следующую страницу.
		skip += len(page.Value)
		more := page.Next != "" || len(page.Value) == substitutionsPage || (page.Count != nil && int64(skip) < *page.Count)
		if len(page.Value) == 0 || !more {
			break
		}
	}
	out := make([]Substitution, 0, len(byUser))
	for _, sb := range byUser {
		out = append(out, sb)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].User.Name != out[j].User.Name {
			return out[i].User.Name < out[j].User.Name
		}
		return out[i].User.ID < out[j].User.ID
	})
	return out, nil
}

// activeAt действует ли замещение в момент now. Даты включительные: RX отдаёт их полночью
// в поясе сервера, и замещение «по 31.12» действует до конца 31.12 в том же поясе.
func (sb Substitution) activeAt(now time.Time) bool {
	if sb.Start != nil && now.Before(*sb.Start) {
		return false
	}
	return sb.End == nil || now.Before(dayAfter(*sb.End))
}

// dayAfter конец дня даты: полночь следующих суток в её поясе. Дата со временем
// считается точным моментом.
func dayAfter(t time.Time) time.Time {
	if h, m, s := t.Clock(); h == 0 && m == 0 && s == 0 && t.Nanosecond() == 0 {
		return t.AddDate(0, 0, 1)
	}
	return t
}

// endsLater кончается ли a позже b; без даты окончания значит бессрочно.
func endsLater(a, b *time.Time) bool {
	switch {
	case a == nil:
		return b != nil
	case b == nil:
		return false
	}
	return a.After(*b)
}

// SubstitutionList «Сидорова Анна (#12) по 31.12.2026; Орлов Игорь (#17) бессрочно».
func (f Formatter) SubstitutionList(subs []Substitution) string {
	parts := make([]string, 0, len(subs))
	for _, sb := range subs {
		p := fmt.Sprintf("%s (#%d)", name(&sb.User), sb.User.ID)
		if sb.End != nil {
			p += " по " + f.Date(sb.End)
		} else {
			p += " бессрочно"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "; ")
}
