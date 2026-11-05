package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/drxinfra/rxmcp/internal/rx"
)

// maxSubstituted сколько замещаемых попадает в фильтр заданий: каждый добавляет условие
// в адрес запроса, а длину адреса ограничивают веб-серверы перед RX.
const maxSubstituted = 30

// substituted готовит include_substituted: Id тех, кого пользователь замещает, и строку
// для ответа, чьи задания добавлены. what называет, что показано: «задания», «уведомления».
// Сбой справочника замещений инструмент не роняет: показываются свои задания, а строка
// говорит почему.
func (s *Server) substituted(ctx context.Context, what string) ([]int64, string) {
	subs, err := s.svc.Substitutions(ctx)
	switch {
	case errors.Is(err, rx.ErrSubstitutionsUnavailable):
		s.log.Info("substitutions unavailable", "err", err)
		return nil, "Замещения в этом RX не читаются: справочника «Замещения» нет в сервисе интеграции или на него нет прав. Показаны только ваши " + what + "."
	case err != nil:
		return nil, fmt.Sprintf("Замещения прочитать не удалось (%v). Показаны только ваши %s.", err, what)
	case len(subs) == 0:
		// Пустой ответ не значит, что замещений нет: обычному пользователю сервис интеграции
		// может не показывать записи справочника вовсе (204 даже без фильтра). Отличить
		// одно от другого нельзя, поэтому говорим, что именно вернул RX.
		return nil, "RX не вернул действующих замещений на вас: показаны только ваши " + what + ". Если вы замещаете коллегу, а здесь пусто, проверьте запись в справочнике «Замещения»."
	}
	n := min(len(subs), maxSubstituted)
	ids := make([]int64, 0, n)
	names := make([]string, 0, n)
	for _, sb := range subs[:n] {
		ids = append(ids, sb.User.ID)
		names = append(names, fmt.Sprintf("%s (#%d)", sb.User.Name, sb.User.ID))
	}
	note := "Добавлены " + what + " тех, кого вы замещаете: " + strings.Join(names, ", ")
	if len(subs) > n {
		note += fmt.Sprintf(" (первые %d из %d)", n, len(subs))
	}
	return ids, note + "; их строки помечены «· за <ФИО>»."
}

// substitutesLine строка rx_whoami о том, кого пользователь замещает. Любая ошибка
// (справочника нет, нет прав, сбой связи) строку просто убирает: «кто я» важнее.
func (s *Server) substitutesLine(ctx context.Context) string {
	subs, err := s.svc.Substitutions(ctx)
	if err != nil {
		s.log.Debug("substitutions", "err", err)
		return ""
	}
	if len(subs) == 0 {
		return ""
	}
	return "Замещаете: " + s.svc.F.SubstitutionList(subs) + ". Их задания покажут rx_my_assignments и rx_whats_new с include_substituted=true."
}

// othersPerformer исполнитель задания, если это не текущий пользователь: задание
// выполняется за него, по замещению. nil, если исполнитель свой или неизвестен.
func (s *Server) othersPerformer(ctx context.Context, a *rx.Assignment) *rx.Ref {
	if a.Performer == nil || a.Performer.ID == 0 {
		return nil
	}
	me, err := s.svc.WhoAmI(ctx)
	if err != nil || me.ID == a.Performer.ID {
		return nil
	}
	p := *a.Performer
	if p.Name == "" {
		p.Name = fmt.Sprintf("#%d", p.ID)
	}
	return &p
}
