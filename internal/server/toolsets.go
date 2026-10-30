package server

import (
	"fmt"
	"sort"
	"strings"
)

// Наборы инструментов (RXMCP_TOOLS). По умолчанию включено всё, как раньше. Сокращённый
// набор нужен там, где инструментов слишком много для модели: малая локальная модель
// на 40+ инструментах заметно хуже выбирает нужный, а клиенту, который работает только
// с заданиями и досками, справка и каталог сущностей ни к чему.
//
// core включён всегда: без rx_whoami и заданий остальное теряет смысл. Инструменты записи
// по-прежнему появляются только с RXMCP_ALLOW_WRITE=1, набор их не включает сам.
var toolsets = map[string][]string{
	"core": {
		"rx_whoami", "rx_my_assignments", "rx_get_assignment", "rx_list_tasks", "rx_get_task",
		"rx_find_documents", "rx_get_document", "rx_get_document_text", "rx_find_employees", "rx_whats_new",
		"rx_complete_assignment", "rx_create_simple_task", "rx_abort_task",
		"rx_send_for_acquaintance", "rx_send_for_approval",
	},
	"boards": {
		"rx_boards", "rx_board", "rx_tickets", "rx_ticket", "rx_ticket_file", "rx_ticket_relations", "rx_ticket_history",
		"rx_create_ticket", "rx_update_ticket", "rx_delete_tickets", "rx_create_column",
		"rx_comment_ticket", "rx_edit_comment", "rx_delete_comment",
	},
	"projects": {
		"rx_projects", "rx_project", "rx_project_plans", "rx_project_plan", "rx_project_risks",
		"rx_create_risks", "rx_update_risk", "rx_create_gates", "rx_update_gate",
	},
	"kb":      {"rx_kb_areas", "rx_kb_search", "rx_kb_article", "rx_create_article", "rx_update_article"},
	"help":    {"rx_help_search", "rx_help_topic", "rx_help_toc"},
	"catalog": {"rx_find_entity", "rx_describe_entity", "rx_query", "rx_call_action"},
}

// ToolsetNames имена наборов по алфавиту, для справки и сообщений об ошибке.
func ToolsetNames() []string {
	out := make([]string, 0, len(toolsets))
	for k := range toolsets {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ParseTools разбирает RXMCP_TOOLS: наборы и отдельные инструменты через запятую или пробел,
// например «boards,kb» или «boards rx_query». Пусто или all — все инструменты (nil).
func ParseTools(spec string) (map[string]bool, error) {
	fields := strings.FieldsFunc(spec, func(r rune) bool { return r == ',' || r == ';' || r == ' ' })
	if len(fields) == 0 {
		return nil, nil
	}
	allowed := map[string]bool{}
	for _, n := range toolsets["core"] {
		allowed[n] = true
	}
	for _, f := range fields {
		f = strings.ToLower(strings.TrimSpace(f))
		if f == "all" {
			return nil, nil
		}
		if names, ok := toolsets[f]; ok {
			for _, n := range names {
				allowed[n] = true
			}
			continue
		}
		if known(f) {
			allowed[f] = true
			continue
		}
		return nil, fmt.Errorf("RXMCP_TOOLS: неизвестный набор или инструмент %q; наборы: all, %s (core включён всегда), или имена rx_* через запятую",
			f, strings.Join(ToolsetNames(), ", "))
	}
	return allowed, nil
}

func known(name string) bool {
	for _, names := range toolsets {
		for _, n := range names {
			if n == name {
				return true
			}
		}
	}
	return false
}

// allows сообщает, оставлен ли инструмент: nil означает «все».
func (o Options) allows(name string) bool { return o.Tools == nil || o.Tools[name] }

// applyToolsets убирает инструменты, которых нет в выбранных наборах.
func (s *Server) applyToolsets() {
	if s.opt.Tools == nil {
		return
	}
	var drop []string
	for _, names := range toolsets {
		for _, n := range names {
			if !s.opt.Tools[n] {
				drop = append(drop, n)
			}
		}
	}
	s.MCP.RemoveTools(drop...)
}

// instructionsFor убирает из инструкций строки про инструменты, которых в наборе нет:
// иначе модель будет звать rx_help_search, которого не видит.
func instructionsFor(o Options, text string) string {
	if o.Tools == nil {
		return text
	}
	lines := strings.Split(text, "\n")
	out := lines[:0]
	for _, l := range lines {
		if strings.Contains(l, "rx_help_search") && !o.allows("rx_help_search") {
			continue
		}
		if strings.Contains(l, "rx_find_entity") && !o.allows("rx_find_entity") {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}
