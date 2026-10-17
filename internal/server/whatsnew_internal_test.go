package server

import (
	"testing"
	"time"
)

func TestParseSince(t *testing.T) {
	loc := time.FixedZone("MSK", 3*3600)
	now := time.Date(2026, 10, 17, 11, 30, 0, 0, loc)
	for in, want := range map[string]time.Time{
		"24h":        now.Add(-24 * time.Hour),
		"3d":         now.Add(-72 * time.Hour),
		"1w":         now.Add(-7 * 24 * time.Hour),
		"2ч":         now.Add(-2 * time.Hour),
		"today":      time.Date(2026, 10, 17, 0, 0, 0, 0, loc),
		"2026-10-15": time.Date(2026, 10, 15, 0, 0, 0, 0, loc),
		"15.10.2026": time.Date(2026, 10, 15, 0, 0, 0, 0, loc),
	} {
		got, err := parseSince(in, now, loc)
		if err != nil || !got.Equal(want) {
			t.Errorf("%s: %v %v, ждали %v", in, got, err, want)
		}
	}
	if _, err := parseSince("вчера вечером", now, loc); err == nil {
		t.Error("непонятное since должно давать ошибку")
	}
}
