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

// Риски и контрольные точки проекта. Импорта в RX нет ни из Excel, ни из текста, поэтому
// сгенерированный реестр рисков руководитель проекта переносил руками по одному.

const maxProjectBatch = 30

type risksIn struct {
	ProjectID int64 `json:"project_id" jsonschema:"Id проекта: число после # в строке rx_projects"`
}

type riskIn struct {
	Name            string `json:"name" jsonschema:"название риска: коротко, что может случиться"`
	Description     string `json:"description" jsonschema:"описание: причины, признаки, последствия; в RX обязательно"`
	Probability     string `json:"probability" jsonschema:"вероятность: название из справочника (например «Высокая»), процент или Id; справочник показывает rx_project_risks"`
	Impact          string `json:"impact" jsonschema:"влияние: название из справочника (например «Средние») или Id"`
	Category        string `json:"category,omitempty" jsonschema:"категория: название из справочника (например «Ресурсный») или Id"`
	Type            string `json:"type,omitempty" jsonschema:"risk (риск, по умолчанию) или possibility (возможность)"`
	Status          string `json:"status,omitempty" jsonschema:"статус: идентифицирован (по умолчанию), отслеживается, предотвращается, реализовался, устраняется, устранён, не актуален"`
	MitigationPlan  string `json:"mitigation_plan,omitempty" jsonschema:"план предотвращения: меры, чтобы риск не случился"`
	ContingencyPlan string `json:"contingency_plan,omitempty" jsonschema:"план устранения последствий, если риск случится"`
	Responsible     string `json:"responsible,omitempty" jsonschema:"ответственный: фамилия или Id сотрудника; по умолчанию руководитель проекта"`
}

type createRisksIn struct {
	ProjectID int64    `json:"project_id" jsonschema:"Id проекта: число после # в строке rx_projects"`
	Risks     []riskIn `json:"risks" jsonschema:"риски, до 30 за вызов"`
}

type updateRiskIn struct {
	ID              int64  `json:"id" jsonschema:"Id риска: число после # в rx_project_risks"`
	Name            string `json:"name,omitempty" jsonschema:"новое название; не передавайте, если не меняется"`
	Description     string `json:"description,omitempty" jsonschema:"новое описание целиком"`
	Probability     string `json:"probability,omitempty" jsonschema:"вероятность: название, процент или Id"`
	Impact          string `json:"impact,omitempty" jsonschema:"влияние: название или Id"`
	Category        string `json:"category,omitempty" jsonschema:"категория: название или Id"`
	Type            string `json:"type,omitempty" jsonschema:"risk или possibility"`
	Status          string `json:"status,omitempty" jsonschema:"статус: идентифицирован, отслеживается, предотвращается, реализовался, устраняется, устранён, не актуален"`
	MitigationPlan  string `json:"mitigation_plan,omitempty" jsonschema:"план предотвращения целиком"`
	ContingencyPlan string `json:"contingency_plan,omitempty" jsonschema:"план устранения последствий целиком"`
	Responsible     string `json:"responsible,omitempty" jsonschema:"ответственный: фамилия или Id"`
}

type gateIn struct {
	Name        string `json:"name" jsonschema:"название: ожидаемый результат кратко, например «Утверждено ТЗ»"`
	PlanDate    string `json:"plan_date" jsonschema:"дата (план): ГГГГ-ММ-ДД или ДД.ММ.ГГГГ"`
	Description string `json:"description,omitempty" jsonschema:"описание ожидаемого результата и критерии достижения"`
	Level       string `json:"level,omitempty" jsonschema:"уровень контроля: КТ-0, КТ-1 или КТ-2"`
	Performer   string `json:"performer,omitempty" jsonschema:"ответственный: фамилия или Id; по умолчанию руководитель проекта"`
	Owner       string `json:"owner,omitempty" jsonschema:"владелец: фамилия или Id; по умолчанию руководитель проекта"`
	Supervisor  string `json:"supervisor,omitempty" jsonschema:"контролёр: фамилия или Id"`
	Assessment  string `json:"assessment,omitempty" jsonschema:"прогноз: в срок, есть несущественные риски, под угрозой"`
	Note        string `json:"note,omitempty" jsonschema:"примечание"`
}

type createGatesIn struct {
	ProjectID int64    `json:"project_id" jsonschema:"Id проекта: число после # в строке rx_projects"`
	Gates     []gateIn `json:"gates" jsonschema:"контрольные точки, до 30 за вызов"`
}

type updateGateIn struct {
	ID          int64  `json:"id" jsonschema:"Id контрольной точки: число после # в блоке «Контрольные точки» rx_project"`
	Name        string `json:"name,omitempty" jsonschema:"новое название; не передавайте, если не меняется"`
	Description string `json:"description,omitempty" jsonschema:"новое описание целиком"`
	PlanDate    string `json:"plan_date,omitempty" jsonschema:"новая дата (план): ГГГГ-ММ-ДД"`
	ActualDate  string `json:"actual_date,omitempty" jsonschema:"дата (факт), а пока КТ не достигнута, прогнозная дата: ГГГГ-ММ-ДД или «сегодня»"`
	Passed      *bool  `json:"passed,omitempty" jsonschema:"true = контрольная точка достигнута (вместе с actual_date)"`
	Level       string `json:"level,omitempty" jsonschema:"уровень контроля: КТ-0, КТ-1 или КТ-2"`
	Assessment  string `json:"assessment,omitempty" jsonschema:"прогноз: в срок, есть несущественные риски, под угрозой"`
	Performer   string `json:"performer,omitempty" jsonschema:"ответственный: фамилия или Id"`
	Owner       string `json:"owner,omitempty" jsonschema:"владелец: фамилия или Id"`
	Supervisor  string `json:"supervisor,omitempty" jsonschema:"контролёр: фамилия или Id"`
	Note        string `json:"note,omitempty" jsonschema:"примечание"`
}

func (r riskIn) input() rx.RiskInput {
	return rx.RiskInput{
		Name: r.Name, Type: r.Type, Status: r.Status, Description: r.Description,
		MitigationPlan: r.MitigationPlan, ContingencyPlan: r.ContingencyPlan,
		Category: r.Category, Probability: r.Probability, Impact: r.Impact, Responsible: r.Responsible,
	}
}

// optDate разбирает дату, если она передана.
func optDate(s string, loc *time.Location) (*time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	t, err := parseDeadline(s, loc)
	if err != nil {
		return nil, fmt.Errorf("дата %q: ожидается ГГГГ-ММ-ДД, ДД.ММ.ГГГГ, «сегодня» или «через N дней»", s)
	}
	return &t, nil
}

// projectLabel «проект «Название» (#Id)» для подтверждений.
func (s *Server) projectLabel(ctx context.Context, id int64) (string, error) {
	p, err := s.svc.ProjectHead(ctx, id)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("проект «%s» (#%d)", p.Name, p.ID), nil
}

func names[T any](items []T, name func(T) string) string {
	var out []string
	for i, it := range items {
		if i == 5 {
			out = append(out, fmt.Sprintf("и ещё %d", len(items)-5))
			break
		}
		out = append(out, "«"+name(it)+"»")
	}
	return strings.Join(out, ", ")
}

func (s *Server) registerProjectRead() {
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_project_risks",
		Description: "Риски проекта, как в реестре рисков карточки проекта: Id, название, статус, категория, вероятность, влияние, ответственный, описание и планы предотвращения и устранения последствий. В конце справочники вероятности, влияния, категорий и статусов: по ним заполняются rx_create_risks и rx_update_risk. Контрольные точки проекта показывает rx_project. Только чтение.",
		Annotations: ro("Риски проекта"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in risksIn) (*mcp.CallToolResult, any, error) {
		rs, err := s.svc.ProjectRisks(ctx, in.ProjectID)
		if err != nil {
			return fail(err)
		}
		refs, err := s.svc.RiskRefs(ctx)
		if err != nil {
			refs = nil // список рисков важнее справочников
		}
		return text(s.svc.F.Risks(in.ProjectID, rs, refs)), nil, nil
	})
}

func (s *Server) registerProjectWrite() {
	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_create_risks",
		Description: "Создать риски в реестре рисков проекта, до 30 за вызов: так сгенерированный реестр переносится в RX без ручного ввода. У каждого риска обязательны название, описание, вероятность и влияние; вероятность, влияние и категорию берите из справочников rx_project_risks. Ответственный по умолчанию руководитель проекта, статус «Идентифицирован». Ввод проверяется целиком до записи. Перед созданием посмотрите rx_project_risks, чтобы не завести дубли. Нужны права на изменение проекта. Меняет данные в RX. Перед вызовом покажите пользователю список рисков.",
		Annotations: rw("Создать риски проекта", false),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in createRisksIn) (*mcp.CallToolResult, any, error) {
		if len(in.Risks) == 0 {
			return fail(errors.New("передайте хотя бы один риск в risks"))
		}
		if len(in.Risks) > maxProjectBatch {
			return fail(fmt.Errorf("за один вызов не больше %d рисков, передано %d: разбейте на части", maxProjectBatch, len(in.Risks)))
		}
		proj, err := s.projectLabel(ctx, in.ProjectID)
		if err != nil {
			return fail(err)
		}
		if ok, err := s.confirm(ctx, req, fmt.Sprintf("Создать %d рисков, %s: %s?", len(in.Risks), proj, names(in.Risks, func(r riskIn) string { return r.Name }))); err != nil || !ok {
			return declined(err)
		}
		inputs := make([]rx.RiskInput, 0, len(in.Risks))
		for _, r := range in.Risks {
			inputs = append(inputs, r.input())
		}
		created, err := s.svc.CreateRisks(ctx, in.ProjectID, inputs)
		var b strings.Builder
		if len(created) > 0 {
			fmt.Fprintf(&b, "Создано рисков: %d, %s:\n", len(created), proj)
			for _, c := range created {
				fmt.Fprintf(&b, "  #%d %s\n", c.ID, c.Name)
			}
			s.log.Info("risks created", "project", in.ProjectID, "count", len(created))
		}
		if err != nil {
			if len(created) > 0 {
				fmt.Fprintf(&b, "Остальные не созданы. Ошибка: %v\nПовторите вызов только с несозданными рисками.", err)
			} else {
				fmt.Fprintf(&b, "Ошибка: %v", err)
			}
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: strings.TrimSpace(b.String())}}}, nil, nil
		}
		b.WriteString("Реестр рисков: rx_project_risks.")
		return text(b.String()), nil, nil
	})

	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_update_risk",
		Description: "Изменить риск проекта: статус, описание, планы предотвращения и устранения последствий, вероятность, влияние, категорию, ответственного, название. Меняются только переданные поля; текстовые поля заменяются целиком. Id риска из rx_project_risks. Меняет данные в RX. Перед вызовом перескажите пользователю, что меняется.",
		Annotations: rw("Изменить риск", false),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in updateRiskIn) (*mcp.CallToolResult, any, error) {
		cur, err := s.svc.RiskByID(ctx, in.ID)
		if err != nil {
			return fail(err)
		}
		r := riskIn{Name: in.Name, Description: in.Description, Probability: in.Probability, Impact: in.Impact, Category: in.Category,
			Type: in.Type, Status: in.Status, MitigationPlan: in.MitigationPlan, ContingencyPlan: in.ContingencyPlan, Responsible: in.Responsible}
		if ok, err := s.confirm(ctx, req, fmt.Sprintf("Изменить риск #%d «%s»?", cur.ID, cur.Name)); err != nil || !ok {
			return declined(err)
		}
		what, err := s.svc.UpdateRisk(ctx, in.ID, r.input())
		if err != nil {
			return fail(err)
		}
		s.log.Info("risk updated", "id", in.ID)
		return text(fmt.Sprintf("Риск #%d «%s» изменён: %s.", cur.ID, cur.Name, strings.Join(what, ", "))), nil, nil
	})

	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_create_gates",
		Description: "Создать контрольные точки (цели проекта) и добавить их в карточку проекта, до 30 за вызов. У каждой обязательны название и плановая дата; уровень КТ-0, КТ-1, КТ-2, описание ожидаемого результата, ответственный, владелец и контролёр по желанию. Владелец и ответственный по умолчанию руководитель проекта. Ввод проверяется целиком до записи. Если карточка проекта открыта кем-то на изменение, RX откажет: её нужно закрыть. Нужны права на изменение проекта. Текущие КТ проекта показывает rx_project. Меняет данные в RX. Перед вызовом покажите пользователю список КТ с датами.",
		Annotations: rw("Создать контрольные точки", false),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in createGatesIn) (*mcp.CallToolResult, any, error) {
		if len(in.Gates) == 0 {
			return fail(errors.New("передайте хотя бы одну контрольную точку в gates"))
		}
		if len(in.Gates) > maxProjectBatch {
			return fail(fmt.Errorf("за один вызов не больше %d КТ, передано %d: разбейте на части", maxProjectBatch, len(in.Gates)))
		}
		inputs := make([]rx.GateInput, 0, len(in.Gates))
		for i, g := range in.Gates {
			if strings.TrimSpace(g.PlanDate) == "" {
				return fail(fmt.Errorf("КТ %d «%s»: нужна плановая дата (plan_date)", i+1, g.Name))
			}
			pd, err := optDate(g.PlanDate, s.svc.F.Loc)
			if err != nil {
				return fail(fmt.Errorf("КТ %d «%s»: %w", i+1, g.Name, err))
			}
			inputs = append(inputs, rx.GateInput{
				Name: g.Name, Description: g.Description, Note: g.Note, Level: g.Level, Assessment: g.Assessment,
				PlanDate: pd, Owner: g.Owner, Performer: g.Performer, Supervisor: g.Supervisor,
			})
		}
		proj, err := s.projectLabel(ctx, in.ProjectID)
		if err != nil {
			return fail(err)
		}
		if ok, err := s.confirm(ctx, req, fmt.Sprintf("Создать %d КТ, %s: %s?", len(in.Gates), proj, names(in.Gates, func(g gateIn) string { return g.Name + " к " + g.PlanDate }))); err != nil || !ok {
			return declined(err)
		}
		created, err := s.svc.CreateGates(ctx, in.ProjectID, inputs)
		var b strings.Builder
		if len(created) > 0 {
			fmt.Fprintf(&b, "Создано КТ: %d, добавлены в %s:\n", len(created), proj)
			for i, c := range created {
				fmt.Fprintf(&b, "  #%d %s · план %s\n", c.ID, c.Name, s.svc.F.Date(inputs[i].PlanDate))
			}
			s.log.Info("gates created", "project", in.ProjectID, "count", len(created))
		}
		if err != nil {
			if len(created) > 0 {
				fmt.Fprintf(&b, "Остальные не созданы. Ошибка: %v\nПовторите вызов только с несозданными КТ.", err)
			} else {
				fmt.Fprintf(&b, "Ошибка: %v", err)
			}
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: strings.TrimSpace(b.String())}}}, nil, nil
		}
		b.WriteString("Контрольные точки проекта: rx_project.")
		return text(b.String()), nil, nil
	})

	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_update_gate",
		Description: "Изменить контрольную точку проекта: отметить достигнутой (passed=true и actual_date), поменять прогноз, плановую или прогнозную дату, описание, уровень, ответственного, владельца, контролёра. Меняются только переданные поля; строку в карточке проекта RX обновит сам. Id КТ из блока «Контрольные точки» rx_project. Меняет данные в RX. Перед вызовом перескажите пользователю, что меняется.",
		Annotations: rw("Изменить контрольную точку", false),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in updateGateIn) (*mcp.CallToolResult, any, error) {
		pd, err := optDate(in.PlanDate, s.svc.F.Loc)
		if err != nil {
			return fail(err)
		}
		ad, err := optDate(in.ActualDate, s.svc.F.Loc)
		if err != nil {
			return fail(err)
		}
		cur, err := s.svc.GateByID(ctx, in.ID)
		if err != nil {
			return fail(err)
		}
		if ok, err := s.confirm(ctx, req, fmt.Sprintf("Изменить контрольную точку #%d «%s»?", cur.ID, cur.Name)); err != nil || !ok {
			return declined(err)
		}
		what, err := s.svc.UpdateGate(ctx, cur, rx.GateInput{
			Name: in.Name, Description: in.Description, Note: in.Note, Level: in.Level, Assessment: in.Assessment,
			PlanDate: pd, ActualDate: ad, Passed: in.Passed, Owner: in.Owner, Performer: in.Performer, Supervisor: in.Supervisor,
		})
		if err != nil {
			return fail(err)
		}
		s.log.Info("gate updated", "id", in.ID)
		return text(fmt.Sprintf("Контрольная точка #%d «%s» изменена: %s.", cur.ID, cur.Name, strings.Join(what, ", "))), nil, nil
	})
}
