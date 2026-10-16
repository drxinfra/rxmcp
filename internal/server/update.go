package server

import (
	"context"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// updateNotice держит новость о вышедшей версии и отдаёт её один раз.
// Проверка идёт в фоне при старте; пока она не закончилась, сказать нечего,
// и новость уйдёт с одним из следующих вызовов.
type updateNotice struct {
	mu     sync.Mutex
	latest string
	shown  bool
}

// SetUpdate сообщает серверу, что вышла версия latest. Вызывается из фоновой проверки.
func (s *Server) SetUpdate(latest string) {
	s.upd.mu.Lock()
	s.upd.latest = latest
	s.upd.mu.Unlock()
}

// updateText строка для пользователя о новой версии; пусто, если новости нет.
func (s *Server) updateText() string {
	s.upd.mu.Lock()
	defer s.upd.mu.Unlock()
	if s.upd.latest == "" {
		return ""
	}
	return fmt.Sprintf("Вышла rxmcp %s (сейчас %s). Обновить: `rxmcp update` в терминале, затем перезапустить ИИ-клиент. Что нового: https://github.com/drxinfra/rxmcp/releases", s.upd.latest, s.opt.Version)
}

// takeUpdate отдаёт новость один раз за процесс.
func (s *Server) takeUpdate() string {
	t := s.updateText()
	if t == "" {
		return ""
	}
	s.upd.mu.Lock()
	defer s.upd.mu.Unlock()
	if s.upd.shown {
		return ""
	}
	s.upd.shown = true
	return t
}

// updateMiddleware дописывает новость к ответу первого инструмента после того,
// как она появилась. Так её увидит пользователь любого клиента: уведомления MCP
// большинство клиентов не показывает.
func (s *Server) updateMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		res, err := next(ctx, method, req)
		if err != nil || method != "tools/call" {
			return res, err
		}
		r, ok := res.(*mcp.CallToolResult)
		// К ошибке не дописываем: модель занята ею и новость потеряется.
		if !ok || r == nil || r.IsError {
			return res, err
		}
		if t := s.takeUpdate(); t != "" {
			r.Content = append(r.Content, &mcp.TextContent{Text: "Служебное от rxmcp, передайте пользователю: " + t})
		}
		return res, err
	}
}
