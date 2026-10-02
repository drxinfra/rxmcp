package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/drxinfra/rxmcp/internal/help"
	"github.com/drxinfra/rxmcp/internal/textx"
)

// HelpSource откуда брать справку: файл индекса и способ скачать её со стенда.
type HelpSource struct {
	Path  string     // файл индекса в каталоге настроек
	Base  string     // адрес каталога справки на стенде
	Fetch help.Fetch // чтение страницы справки
}

// helpState лениво загружает индекс и, если его нет, один раз строит его в фоне.
type helpState struct {
	src HelpSource

	mu       sync.Mutex
	ix       *help.Index
	building bool
	done     int
	total    int
	lastErr  error
}

// index возвращает готовый индекс или сообщение для пользователя, почему его пока нет.
func (h *helpState) index() (*help.Index, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ix != nil {
		return h.ix, ""
	}
	if h.building {
		if h.total > 0 {
			return nil, fmt.Sprintf("Справка ещё скачивается со стенда: %d из %d статей. Повторите запрос через минуту.", h.done, h.total)
		}
		return nil, "Справка ещё скачивается со стенда. Повторите запрос через минуту."
	}
	ix, err := help.Load(h.src.Path)
	if err == nil {
		h.ix = ix
		return ix, ""
	}
	if !errors.Is(err, help.ErrNoIndex) {
		return nil, "Индекс справки не читается: " + err.Error()
	}
	if h.lastErr != nil {
		msg := "Справку не удалось скачать со стенда: " + h.lastErr.Error() + ". Адрес каталога справки можно задать явно: rxmcp config set RXMCP_HELP_URL=https://…/WebHelp/ru-RU, затем rxmcp docs index."
		h.lastErr = nil // следующая попытка начнётся со следующего вызова
		return nil, msg
	}
	if h.src.Fetch == nil {
		return nil, "Справка не проиндексирована. Выполните в терминале: rxmcp docs index"
	}
	h.building = true
	go h.build()
	return nil, "Справка этого стенда ещё не скачана. Начал скачивать её с " + h.src.Base + ": это разовая операция на несколько минут. Повторите запрос позже, остальные инструменты работают как обычно."
}

func (h *helpState) build() {
	ix, err := help.Crawl(context.Background(), h.src.Fetch, h.src.Base, 6, func(done, total int) {
		h.mu.Lock()
		h.done, h.total = done, total
		h.mu.Unlock()
	})
	if err == nil {
		err = ix.Save(h.src.Path)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.building = false
	if err != nil {
		h.lastErr = err
		return
	}
	h.ix = ix
}

type helpSearchIn struct {
	Query   string `json:"query" jsonschema:"что ищем, обычными словами: «как настроить правило согласования», «замещение», «права доступа на папку»"`
	Section string `json:"section,omitempty" jsonschema:"подстрока раздела оглавления, чтобы сузить поиск: «Администрирование», «Договоры»"`
	Limit   int    `json:"limit,omitempty" jsonschema:"сколько статей показать, по умолчанию 8, максимум 20"`
}

type helpTopicIn struct {
	Topic    string `json:"topic" jsonschema:"имя файла статьи из результатов поиска или из ссылки в тексте, например sungero_approval_rule.htm"`
	MaxChars int    `json:"max_chars,omitempty" jsonschema:"лимит символов, по умолчанию из настроек сервера"`
	Offset   int    `json:"offset,omitempty" jsonschema:"с какого символа продолжить, если статья обрезана"`
}

type helpTocIn struct {
	Section string `json:"section,omitempty" jsonschema:"подстрока раздела; пусто = верхний уровень оглавления"`
	Depth   int    `json:"depth,omitempty" jsonschema:"сколько уровней вглубь показать, по умолчанию 1"`
}

func (s *Server) registerHelp() {
	h := &helpState{src: *s.opt.Help}

	mcp.AddTool(s.MCP, &mcp.Tool{
		Name: "rx_help_search",
		Description: "Поиск по справке Directum RX этого стенда: как устроен механизм, как что-то настроить, что значит поле или статус. " +
			"Справка той же версии, что и система пользователя. Ищет по словам с учётом окончаний; если мало результатов, переформулируйте запрос терминами системы. " +
			"Возвращает статьи с фрагментом, полный текст открывается через rx_help_topic.",
		Annotations: ro("Поиск по справке"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in helpSearchIn) (*mcp.CallToolResult, any, error) {
		ix, msg := h.index()
		if ix == nil {
			return text(msg), nil, nil
		}
		if strings.TrimSpace(in.Query) == "" {
			return fail(errors.New("пустой запрос"))
		}
		limit := in.Limit
		if limit <= 0 {
			limit = 8
		}
		if limit > 20 {
			limit = 20
		}
		hits := ix.Search(in.Query, in.Section, limit)
		if len(hits) == 0 {
			return text(fmt.Sprintf("В справке (%s, %d статей) по запросу «%s» ничего не нашлось. Попробуйте другие слова или термины системы.", ix.Product, len(ix.Topics), in.Query)), nil, nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s, найдено по запросу «%s»:\n", ix.Product, in.Query)
		for i, hit := range hits {
			t := hit.Topic
			fmt.Fprintf(&b, "\n%d. %s\n   раздел: %s\n   статья: %s\n", i+1, t.Title, orDash(strings.Join(t.Crumbs, " > ")), t.File)
			if hit.Snippet != "" {
				fmt.Fprintf(&b, "   %s\n", hit.Snippet)
			}
		}
		b.WriteString("\nПолный текст статьи: rx_help_topic с именем файла.")
		return text(b.String()), nil, nil
	})

	mcp.AddTool(s.MCP, &mcp.Tool{
		Name: "rx_help_topic",
		Description: "Полный текст статьи справки Directum RX по имени файла. Ссылки в тексте вида [текст](файл.htm) ведут на другие статьи: " +
			"их можно открыть этим же инструментом, если там описан упомянутый механизм.",
		Annotations: ro("Статья справки"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in helpTopicIn) (*mcp.CallToolResult, any, error) {
		ix, msg := h.index()
		if ix == nil {
			return text(msg), nil, nil
		}
		t := ix.Topic(in.Topic)
		if t == nil {
			return fail(fmt.Errorf("статьи %q в справке нет: возьмите имя файла из rx_help_search", in.Topic))
		}
		limit := in.MaxChars
		if limit <= 0 || limit > s.opt.MaxTextChars*5 {
			limit = s.opt.MaxTextChars
		}
		runes := []rune(t.Text)
		total := len(runes)
		if in.Offset > 0 {
			if in.Offset >= total {
				return text(fmt.Sprintf("Статья %s: offset %d за пределами текста (всего %d символов).", t.File, in.Offset, total)), nil, nil
			}
			runes = runes[in.Offset:]
		}
		out, cut := textx.Cut(string(runes), limit)
		var b strings.Builder
		fmt.Fprintf(&b, "%s\nРаздел: %s\nИсточник: %s (%s)\n", t.Title, orDash(strings.Join(t.Crumbs, " > ")), ix.URL(t), ix.Product)
		if cut {
			fmt.Fprintf(&b, "Показано %d из %d символов, продолжение с offset=%d.\n", limit, total, in.Offset+limit)
		}
		b.WriteString("\n")
		b.WriteString(out)
		return text(b.String()), nil, nil
	})

	mcp.AddTool(s.MCP, &mcp.Tool{
		Name:        "rx_help_toc",
		Description: "Оглавление справки Directum RX: разделы верхнего уровня или содержимое раздела. Помогает понять, где искать, когда поиск по словам не даёт нужного.",
		Annotations: ro("Оглавление справки"),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in helpTocIn) (*mcp.CallToolResult, any, error) {
		ix, msg := h.index()
		if ix == nil {
			return text(msg), nil, nil
		}
		depth := in.Depth
		if depth <= 0 {
			depth = 1
		}
		sec := strings.ToLower(strings.TrimSpace(in.Section))
		var b strings.Builder
		fmt.Fprintf(&b, "%s: %d статей, скачана %s.\n", ix.Product, len(ix.Topics), ix.Built.Format("02.01.2006"))
		n, base := 0, -1
		for i := range ix.Topics {
			t := &ix.Topics[i]
			level := len(t.Crumbs)
			if sec == "" {
				if level >= depth {
					continue
				}
			} else {
				// раздел задаётся первым совпавшим заголовком, показываем его потомков до нужной глубины
				at := -1
				for k, c := range t.Crumbs {
					if strings.Contains(strings.ToLower(c), sec) {
						at = k
						break
					}
				}
				if at < 0 {
					if !strings.Contains(strings.ToLower(t.Title), sec) {
						continue
					}
					at = level
				}
				if base < 0 {
					base = at
				}
				if level-at > depth {
					continue
				}
				level -= base
				if level < 0 {
					level = 0
				}
			}
			if n >= 200 {
				b.WriteString("… список обрезан на 200 строках, уточните раздел.\n")
				break
			}
			fmt.Fprintf(&b, "%s- %s (%s)\n", strings.Repeat("  ", level), t.Title, t.File)
			n++
		}
		if n == 0 {
			b.WriteString("Раздел не найден. Вызовите без section, чтобы увидеть верхний уровень.\n")
		}
		return text(b.String()), nil, nil
	})
}
