package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/drxinfra/rxmcp/internal/rx"
)

type acquaintanceIn struct {
	DocumentID   int64   `json:"document_id" jsonschema:"Id документа: число после # в строке rx_find_documents"`
	PerformerIDs []int64 `json:"performer_ids" jsonschema:"Id сотрудников, которые должны ознакомиться (rx_find_employees)"`
	Deadline     string  `json:"deadline" jsonschema:"срок ознакомления, обязателен: ГГГГ-ММ-ДД, ГГГГ-ММ-ДДTЧЧ:ММ, «завтра» или «через 3 дня»"`
	Text         string  `json:"text,omitempty" jsonschema:"текст задачи: что и зачем прочитать"`
	Electronic   bool    `json:"electronic,omitempty" jsonschema:"true = электронное ознакомление: участник подтверждает его подписью в RX; по умолчанию обычное"`
}

type approvalIn struct {
	DocumentID  int64   `json:"document_id" jsonschema:"Id документа: число после # в строке rx_find_documents"`
	ApproverIDs []int64 `json:"approver_ids,omitempty" jsonschema:"Id согласующих (rx_find_employees); для свободного согласования обязательны, для mode=rule это дополнительные согласующие сверх правила"`
	Mode        string  `json:"mode,omitempty" jsonschema:"free (по умолчанию) = свободное согласование с указанными людьми; rule = по регламенту: маршрут строит правило согласования RX для вида документа"`
	Text        string  `json:"text,omitempty" jsonschema:"текст задачи: что согласовать и на что обратить внимание"`
	Deadline    string  `json:"deadline,omitempty" jsonschema:"срок свободного согласования: ГГГГ-ММ-ДД, «завтра», «через 3 дня»; в mode=rule сроки задаёт правило"`
	SignatoryID int64   `json:"signatory_id,omitempty" jsonschema:"для mode=rule: Id подписанта, если правило его не определяет само"`
}

type createArticleIn struct {
	Title  string `json:"title" jsonschema:"название статьи"`
	Text   string `json:"text" jsonschema:"текст статьи в markdown: заголовки ##, списки, таблицы, ссылки"`
	AreaID int64  `json:"area_id" jsonschema:"Id области знаний из rx_kb_areas"`
}

type updateArticleIn struct {
	ID    int64  `json:"id" jsonschema:"Id статьи из rx_kb_search"`
	Title string `json:"title,omitempty" jsonschema:"новое название; не передавайте, если не меняется"`
	Text  string `json:"text,omitempty" jsonschema:"новый текст целиком в markdown; запишется новой версией, прежняя останется в истории"`
}

// docLabel «#Id «название»» для форм подтверждения; если документ не прочитать,
// RX всё равно ответит ошибкой на само действие.
func (s *Server) docLabel(ctx context.Context, id int64) string {
	if d, err := s.svc.GetDocument(ctx, id); err == nil {
		return fmt.Sprintf("#%d «%s»", d.ID, d.Name)
	}
	return fmt.Sprintf("#%d", id)
}

func (s *Server) registerDocflowWrite() {
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_send_for_acquaintance",
		Description: "Отправить документ на ознакомление сотрудникам: создаёт и стартует задачу на ознакомление, участники получают задания. Нужны Id документа (rx_find_documents), Id сотрудников (rx_find_employees) и срок. electronic=true требует от участников подтвердить ознакомление подписью в RX. Меняет данные в RX. Перед вызовом перескажите пользователю документ, участников и срок.",
		Annotations: rw("Отправить на ознакомление", false),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in acquaintanceIn) (*mcp.CallToolResult, any, error) {
		if in.Deadline == "" {
			return fail(errors.New("нужен срок ознакомления (deadline): спросите у пользователя или предложите через 3 рабочих дня"))
		}
		dl, err := parseDeadline(in.Deadline, s.svc.F.Loc)
		if err != nil {
			return fail(err)
		}
		doc := s.docLabel(ctx, in.DocumentID)
		kind := "ознакомление"
		if in.Electronic {
			kind = "электронное ознакомление (с подписью)"
		}
		if ok, err := s.confirm(ctx, req, fmt.Sprintf("Отправить документ %s на %s сотрудникам %v, срок %s?", doc, kind, in.PerformerIDs, s.svc.F.DateTime(&dl))); err != nil || !ok {
			return declined(err)
		}
		id, err := s.svc.SendForAcquaintance(ctx, rx.AcquaintanceInput{DocumentID: in.DocumentID, PerformerIDs: in.PerformerIDs, Text: in.Text, Electronic: in.Electronic, Deadline: dl})
		if err != nil {
			return fail(err)
		}
		s.log.Info("acquaintance task started", "id", id, "document", in.DocumentID)
		return text(fmt.Sprintf("Документ %s отправлен на ознакомление: задача #%d. Ход ознакомления: rx_get_task.", doc, id)), nil, nil
	})

	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_send_for_approval",
		Description: "Отправить документ на согласование: создаёт и стартует задачу. mode=free (по умолчанию) это свободное согласование с указанными людьми и сроком. mode=rule это согласование по регламенту: маршрут, этапы и сроки строит правило согласования RX для вида документа, approver_ids добавляются сверх правила; если правила нет, RX откажет. Подпись документа этот инструмент не ставит. Меняет данные в RX. Перед вызовом перескажите пользователю документ, режим и согласующих.",
		Annotations: rw("Отправить на согласование", false),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in approvalIn) (*mcp.CallToolResult, any, error) {
		mode := strings.ToLower(strings.TrimSpace(in.Mode))
		byRule := false
		switch mode {
		case "", "free", "свободное":
		case "rule", "регламент", "по регламенту":
			byRule = true
		default:
			return fail(errors.New("mode: free (свободное согласование) или rule (по регламенту)"))
		}
		var dl *time.Time
		if in.Deadline != "" && !byRule {
			t, err := parseDeadline(in.Deadline, s.svc.F.Loc)
			if err != nil {
				return fail(err)
			}
			dl = &t
		}
		doc := s.docLabel(ctx, in.DocumentID)
		msg := fmt.Sprintf("Отправить документ %s на свободное согласование: %v", doc, in.ApproverIDs)
		if dl != nil {
			msg += ", срок " + s.svc.F.DateTime(dl)
		}
		if byRule {
			msg = fmt.Sprintf("Отправить документ %s на согласование по регламенту (маршрут по правилу RX)", doc)
			if len(in.ApproverIDs) > 0 {
				msg += fmt.Sprintf(", дополнительно %v", in.ApproverIDs)
			}
			if in.SignatoryID != 0 {
				msg += fmt.Sprintf(", подписант %d", in.SignatoryID)
			}
		}
		if ok, err := s.confirm(ctx, req, msg+"?"); err != nil || !ok {
			return declined(err)
		}
		id, err := s.svc.SendForApproval(ctx, rx.ApprovalInput{DocumentID: in.DocumentID, ApproverIDs: in.ApproverIDs, Text: in.Text, Deadline: dl, ByRule: byRule, SignatoryID: in.SignatoryID})
		if err != nil {
			return fail(err)
		}
		s.log.Info("approval task started", "id", id, "document", in.DocumentID, "rule", byRule)
		return text(fmt.Sprintf("Документ %s отправлен на согласование: задача #%d. Ход согласования: rx_get_task.", doc, id)), nil, nil
	})

	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_create_article",
		Description: "Создать статью базы знаний в области: название и текст в markdown. Статья создаётся черновиком от имени пользователя, видна по правам области. Нужен Id области из rx_kb_areas. Перед созданием проверьте через rx_kb_search, что такой статьи ещё нет. Меняет данные в RX. Перед вызовом покажите пользователю название и начало текста.",
		Annotations: rw("Создать статью", false),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in createArticleIn) (*mcp.CallToolResult, any, error) {
		if ok, err := s.confirm(ctx, req, fmt.Sprintf("Создать статью «%s» в области %d (%d символов текста)?", in.Title, in.AreaID, len([]rune(in.Text)))); err != nil || !ok {
			return declined(err)
		}
		id, err := s.svc.CreateArticle(ctx, in.Title, in.Text, in.AreaID)
		if err != nil {
			return fail(err)
		}
		s.log.Info("article created", "id", id)
		return text(fmt.Sprintf("Статья «%s» создана: #%d, черновик. Посмотреть: rx_kb_article.", in.Title, id)), nil, nil
	})

	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_update_article",
		Description: "Изменить статью базы знаний: название и (или) текст целиком в markdown. Новый текст записывается новой версией, прежняя остаётся в истории статьи. Сначала прочитайте статью через rx_kb_article и правьте её текст, а не пишите заново. Меняет данные в RX. Перед вызовом перескажите пользователю, что меняется.",
		Annotations: rw("Изменить статью", false),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in updateArticleIn) (*mcp.CallToolResult, any, error) {
		var what []string
		if in.Title != "" {
			what = append(what, fmt.Sprintf("название на «%s»", in.Title))
		}
		if in.Text != "" {
			what = append(what, fmt.Sprintf("текст новой версией (%d символов)", len([]rune(in.Text))))
		}
		if len(what) == 0 {
			return fail(errors.New("нечего менять: передайте title или text"))
		}
		if ok, err := s.confirm(ctx, req, fmt.Sprintf("Изменить статью #%d: %s?", in.ID, strings.Join(what, ", "))); err != nil || !ok {
			return declined(err)
		}
		if err := s.svc.UpdateArticle(ctx, in.ID, in.Title, in.Text); err != nil {
			return fail(err)
		}
		s.log.Info("article updated", "id", in.ID)
		return text(fmt.Sprintf("Статья #%d изменена: %s.", in.ID, strings.Join(what, ", "))), nil, nil
	})
}
