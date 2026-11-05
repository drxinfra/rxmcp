package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Подтверждение записи. Каждый инструмент записи перед действием показывает пользователю
// форму (elicitation) с текстом того, что будет сделано. Как форма доходит до клиента,
// зависит от версии протокола:
//
//   - до 2026-07-28 сервер посреди tools/call шлёт клиенту запрос elicitation/create и ждёт ответа;
//   - с 2026-07-28 сервер запросов клиенту не шлёт (SEP-2322, multi round-trip requests):
//     инструмент отвечает inputRequests с той же формой и requestState, клиент показывает
//     форму и повторяет вызов с ответом. Действие выполняется только на повторе с согласием.
//
// Если клиент форму не заявил, действие выполняется: согласием служит разрешение на вызов
// инструмента в самом клиенте, а запись и так включена явно (RXMCP_ALLOW_WRITE=1).

// mrtrVersion первая версия протокола, где форма уходит в ответе инструмента.
const mrtrVersion = "2026-07-28"

// confirmKey имя формы подтверждения в inputRequests и inputResponses.
const confirmKey = "confirm"

const (
	confirmTTL      = 5 * time.Minute // сколько ждём ответа на форму и помним принятый ответ
	confirmMax      = 1024            // ожиданий на процесс
	confirmMaxOwner = 64              // ожиданий одного пользователя или сеанса
	noFormMax       = 1024            // сеансов, о которых уже сказали в журнал
)

// confirmForm форма подтверждения: одна и та же в обоих путях.
func confirmForm(msg string) *mcp.ElicitParams {
	return &mcp.ElicitParams{
		Mode:    "form",
		Message: msg,
		RequestedSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"confirm": map[string]any{"type": "boolean", "title": "Подтверждаю", "description": "Да, выполнить"},
			},
			"required": []string{"confirm"},
		},
	}
}

// declined ответ на отказ: пользователь отклонил или закрыл форму, в RX ничего не ушло.
func declined() *mcp.CallToolResult {
	return text("Пользователь отменил действие. Ничего не изменено.")
}

// accepted согласие в ответе на форму: «accept» и галочка не снята. Ответ «accept» без
// поля confirm тоже согласие, как и раньше: кнопку подтверждения пользователь нажал сам.
func accepted(res *mcp.ElicitResult) bool {
	if res == nil || res.Action != "accept" {
		return false
	}
	v, ok := res.Content["confirm"]
	return !ok || v == true
}

// confirm спрашивает у пользователя подтверждение записи. nil значит можно выполнять.
// Иначе это ответ, который инструмент возвращает как есть: отказ, ошибка или (на протоколе
// 2026-07-28) форма для клиента. С RXMCP_CONFIRM=0 форму не показываем: согласие даёт сам
// клиент, разрешая вызов инструмента.
func (s *Server) confirm(ctx context.Context, req *mcp.CallToolRequest, msg string) *mcp.CallToolResult {
	if req == nil || req.Session == nil {
		return nil
	}
	if s.opt.NoConfirm {
		s.noForm(req, "RXMCP_CONFIRM=0")
		return nil
	}
	if roundTrip(req) {
		return s.confirmRoundTrip(req, msg)
	}
	return s.confirmInCall(ctx, req, msg)
}

// roundTrip клиент на протоколе 2026-07-28 или новее: запрос посреди вызова SDK ему не
// отправит (Elicit вернёт ошибку). Версия та же, по которой SDK решает, как отдать ответ
// с inputRequests; без initialize SDK тоже считает протокол новым.
func roundTrip(req *mcp.CallToolRequest) bool {
	if p := req.Session.InitializeParams(); p != nil {
		return p.ProtocolVersion >= mrtrVersion
	}
	return true
}

// confirmInCall протокол до 2026-07-28: форма запросом elicitation/create посреди вызова.
func (s *Server) confirmInCall(ctx context.Context, req *mcp.CallToolRequest, msg string) *mcp.CallToolResult {
	caps := req.ClientCapabilities()
	if caps == nil || caps.Elicitation == nil {
		s.noForm(req, "client does not declare elicitation")
		return nil
	}
	res, err := req.Session.Elicit(ctx, confirmForm(msg))
	if err != nil {
		// Хост заявил elicitation, но не смог: не блокируем, флаг записи уже согласие.
		s.log.Warn("elicitation failed", "err", err)
		return nil
	}
	if !accepted(res) {
		return declined()
	}
	return nil
}

// confirmRoundTrip протокол 2026-07-28: форма уходит в ответе инструмента, клиент повторяет
// вызов с ответом и requestState. Первый заход ничего не выполняет. На повторе ожидание
// гасится до действия, поэтому ещё один повтор с тем же requestState действие не повторит,
// а чужой, устаревший или от другого вызова requestState приводит к новой форме.
func (s *Server) confirmRoundTrip(req *mcp.CallToolRequest, msg string) *mcp.CallToolResult {
	if !formSupported(req.ClientCapabilities()) {
		s.noForm(req, "client does not declare elicitation form mode")
		return nil
	}
	owner, call := s.confirmOwner(req), confirmCall(req, msg)
	if state := req.Params.RequestState; state != "" {
		switch s.confirms.take(state, owner, call) {
		case takeOK:
			if ans, ok := req.Params.InputResponses[confirmKey].(*mcp.ElicitResult); ok {
				if accepted(ans) {
					return nil
				}
				return declined()
			}
			// Повтор без ответа на форму: спрашиваем снова, прежний requestState погашен.
		case takeUsed:
			return failResult(errors.New("ответ на это подтверждение уже обработан, второй раз действие не выполняется. Проверьте в RX, выполнено ли оно; если нужно повторить, вызовите инструмент заново, подтверждение будет спрошено снова"))
		}
	}
	return &mcp.CallToolResult{
		InputRequests: mcp.InputRequestMap{confirmKey: confirmForm(msg)},
		RequestState:  s.confirms.open(owner, call),
	}
}

// formSupported клиент заявил форму: elicitation с form или пустой elicitation (так форму
// заявляли до 2025-11-25). Клиенту только с url форму слать нельзя.
func formSupported(caps *mcp.ClientCapabilities) bool {
	if caps == nil || caps.Elicitation == nil {
		return false
	}
	return caps.Elicitation.Form != nil || caps.Elicitation.URL == nil
}

// confirmOwner чей ответ на форму примем. В HTTP-режиме с персональными учётками сервер и
// сеанс живут один запрос, там это пользователь из проверенного токена. Иначе сеанс MCP:
// по stdio он один на процесс, по HTTP с общим секретом у него есть Mcp-Session-Id.
func (s *Server) confirmOwner(req *mcp.CallToolRequest) string {
	if s.opt.User != "" {
		return "user:" + s.opt.User
	}
	if id := req.Session.ID(); id != "" {
		return "session:" + id
	}
	return fmt.Sprintf("conn:%p", req.Session)
}

// confirmCall отпечаток вызова: инструмент, аргументы и текст формы. Ответ применяется
// только к тому вызову, о котором спрашивали, и только если текст формы тот же: если между
// вопросом и ответом в RX что-то изменилось (задание переназначили, срок «завтра» сдвинулся),
// форма будет показана заново с новым текстом.
func confirmCall(req *mcp.CallToolRequest, msg string) [32]byte {
	args := []byte(req.Params.Arguments)
	var v any
	d := json.NewDecoder(bytes.NewReader(args))
	d.UseNumber()
	if d.Decode(&v) == nil {
		if b, err := json.Marshal(v); err == nil {
			args = b // порядок ключей и пробелы у клиента могут быть другими
		}
	}
	h := sha256.New()
	for _, p := range [][]byte{[]byte(req.Params.Name), args, []byte(msg)} {
		h.Write(p)
		h.Write([]byte{0})
	}
	return [32]byte(h.Sum(nil))
}

// noForm пишет в журнал один раз на сеанс, что формы подтверждения не будет и почему:
// запись тогда идёт по разрешению на вызов инструмента в самом клиенте.
func (s *Server) noForm(req *mcp.CallToolRequest, reason string) {
	key := req.Session.ID()
	s.noFormMu.Lock()
	if _, ok := s.noFormSeen[key]; ok {
		s.noFormMu.Unlock()
		return
	}
	if len(s.noFormSeen) >= noFormMax {
		clear(s.noFormSeen)
	}
	s.noFormSeen[key] = struct{}{}
	s.noFormMu.Unlock()
	client := ""
	if ci := req.ClientInfo(); ci != nil {
		client = ci.Name
	}
	s.log.Info("no write confirmation form, relying on the client's tool permission prompt",
		"reason", reason, "protocol", req.ProtocolVersion(), "client", client)
}

// Confirmations ожидания ответа на форму подтверждения (протокол 2026-07-28). Между первым
// заходом и повтором сервер держит только эту запись, а requestState это её случайный
// ключ: подделать его нельзя, а чужой или устаревший просто не найдётся. Запись привязана
// к пользователю или сеансу и к отпечатку вызова, ждёт ответа confirmTTL и срабатывает
// один раз; после ответа она помнится ещё confirmTTL, чтобы повтор не выполнил действие
// второй раз. Размер ограничен: confirmMaxOwner на владельца и confirmMax всего.
//
// Склад один на процесс: в HTTP-режиме с персональными учётками сервер собирается на
// каждый запрос, и повтор с ответом приходит уже в другой экземпляр. Несколько процессов
// за балансировщиком склад не делят: повтор, попавший в другой процесс, получит форму заново.
type Confirmations struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]*confirmEntry
}

type confirmEntry struct {
	owner   string
	call    [32]byte
	expires time.Time
	used    bool // ответ принят: повтор с тем же ключом действие не выполняет
}

type takeResult int

const (
	takeUnknown takeResult = iota // нет такого ожидания: чужое, истекло или вызов другой
	takeOK                        // ожидание погашено, ответ можно применить один раз
	takeUsed                      // ответ по этому ожиданию уже принят
)

// NewConfirmations пустой склад ожиданий.
func NewConfirmations() *Confirmations {
	return &Confirmations{now: time.Now, entries: map[string]*confirmEntry{}}
}

// processConfirmations склад по умолчанию, общий для всех серверов процесса.
var processConfirmations = NewConfirmations()

// open заводит ожидание и возвращает ключ для requestState.
func (c *Confirmations) open(owner string, call [32]byte) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.purge(now)
	c.evict(owner)
	id := rand.Text()
	c.entries[id] = &confirmEntry{owner: owner, call: call, expires: now.Add(confirmTTL)}
	return id
}

// take гасит ожидание id, если его завёл тот же владелец для того же вызова.
func (c *Confirmations) take(id, owner string, call [32]byte) takeResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.purge(now)
	e, ok := c.entries[id]
	if !ok || e.owner != owner || e.call != call {
		return takeUnknown
	}
	if e.used {
		return takeUsed
	}
	e.used = true
	e.expires = now.Add(confirmTTL)
	return takeOK
}

// purge убирает истёкшие записи.
func (c *Confirmations) purge(now time.Time) {
	for id, e := range c.entries {
		if !now.Before(e.expires) {
			delete(c.entries, id)
		}
	}
}

// evict освобождает место под новую запись владельца: убирает его самую старую, если у
// него их уже confirmMaxOwner, иначе самую старую вообще, если склад полон. Так один
// пользователь не вытеснит ожидания других.
func (c *Confirmations) evict(owner string) {
	n := 0
	var own, oldest *confirmEntry
	var ownID, oldestID string
	for id, e := range c.entries {
		if e.owner == owner {
			n++
			if own == nil || e.expires.Before(own.expires) {
				own, ownID = e, id
			}
		}
		if oldest == nil || e.expires.Before(oldest.expires) {
			oldest, oldestID = e, id
		}
	}
	switch {
	case n >= confirmMaxOwner:
		delete(c.entries, ownID)
	case len(c.entries) >= confirmMax:
		delete(c.entries, oldestID)
	}
}
