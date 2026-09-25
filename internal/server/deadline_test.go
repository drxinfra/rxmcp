package server

import (
	"testing"
	"time"
)

func TestParseDeadlineForms(t *testing.T) {
	loc := time.UTC
	now := time.Now().In(loc)
	cases := []struct {
		in   string
		want string // ожидаемая дата, ГГГГ-ММ-ДД
		hour int
	}{
		{"2026-10-03", "2026-10-03", 18},
		{"2026-10-03T09:30", "2026-10-03", 9},
		{"03.10.2026", "2026-10-03", 18},
		{"сегодня", now.Format("2006-01-02"), 18},
		{"завтра", now.AddDate(0, 0, 1).Format("2006-01-02"), 18},
		{"через 3 дня", now.AddDate(0, 0, 3).Format("2006-01-02"), 18},
		{"+2д", now.AddDate(0, 0, 2).Format("2006-01-02"), 18},
	}
	for _, c := range cases {
		got, err := parseDeadline(c.in, loc)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got.Format("2006-01-02") != c.want || got.Hour() != c.hour {
			t.Errorf("%q -> %s, ожидалось %s %02d:00", c.in, got.Format("2006-01-02 15:04"), c.want, c.hour)
		}
	}
	if _, err := parseDeadline("когда-нибудь", loc); err == nil {
		t.Error("непонятный срок должен быть ошибкой, а не тихой датой")
	}
}
