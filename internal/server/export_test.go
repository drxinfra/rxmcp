package server

import "time"

// SetNow подменяет часы склада подтверждений: тесты проверяют срок ожидания без sleep.
func (c *Confirmations) SetNow(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// ConfirmTTL сколько ждём ответа на форму.
const ConfirmTTL = confirmTTL
