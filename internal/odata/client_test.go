package odata

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMergeCookies(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		header  string
		set     []*http.Cookie
		want    string
		changed bool
	}{
		{"замена значения", "sungero_client=OLD==", []*http.Cookie{{Name: "sungero_client", Value: "NEW=="}}, "sungero_client=NEW==", true},
		{"соседние куки целы", "a=1; sungero_client=OLD; b=2", []*http.Cookie{{Name: "sungero_client", Value: "NEW"}}, "a=1; sungero_client=NEW; b=2", true},
		{"то же значение", "sungero_client=X", []*http.Cookie{{Name: "sungero_client", Value: "X"}}, "sungero_client=X", false},
		{"чужая кука не добавляется", "sungero_client=X", []*http.Cookie{{Name: "lb", Value: "node1"}}, "sungero_client=X", false},
		{"удаление пустым значением", "sungero_client=X", []*http.Cookie{{Name: "sungero_client", Value: ""}}, "sungero_client=X", false},
		{"удаление MaxAge", "sungero_client=X", []*http.Cookie{{Name: "sungero_client", Value: "Y", MaxAge: -1}}, "sungero_client=X", false},
		{"удаление истёкшим сроком", "sungero_client=X", []*http.Cookie{{Name: "sungero_client", Value: "Y", Expires: now.Add(-time.Hour)}}, "sungero_client=X", false},
		{"продление со сроком", "sungero_client=X", []*http.Cookie{{Name: "sungero_client", Value: "Y", Expires: now.Add(time.Hour)}}, "sungero_client=Y", true},
	}
	for _, c := range cases {
		got, changed := MergeCookies(c.header, c.set, now)
		if got != c.want || changed != c.changed {
			t.Errorf("%s: получили %q, %v; ждали %q, %v", c.name, got, changed, c.want, c.changed)
		}
	}
}

func TestClientSavesRenewedCookie(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("sungero_client"); err != nil || c.Value != "OLD" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "sungero_client", Value: "NEW", Path: "/", HttpOnly: true})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":[{"Id":1}]}`))
	}))
	defer srv.Close()
	var saved string
	cl, err := New(Options{
		BaseURL:    srv.URL + "/odata",
		Auth:       "cookie",
		CookieFunc: func() (string, error) { return "sungero_client=OLD", nil },
		CookieSave: func(s string) error { saved = s; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.List(t.Context(), "IUsers", Query{Top: 1}); err != nil {
		t.Fatal(err)
	}
	if saved != "sungero_client=NEW" || cl.Renewed() != 1 {
		t.Fatalf("сохранено %q, продлений %d", saved, cl.Renewed())
	}
}

func TestClientRenewsOn401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("sungero_client"); err != nil || c.Value != "FRESH" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	cookie, renews := "sungero_client=STALE", 0
	cl, err := New(Options{
		BaseURL:    srv.URL + "/odata",
		Auth:       "cookie",
		CookieFunc: func() (string, error) { return cookie, nil },
		CookieRenew: func(context.Context) error {
			renews++
			cookie = "sungero_client=FRESH"
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// POST с телом: повтор должен отправить тело заново.
	if _, err := cl.Action(t.Context(), "Mod", "Act", map[string]any{"a": 1}); err != nil {
		t.Fatalf("после продления: %v", err)
	}
	if renews != 1 {
		t.Fatalf("продлений %d", renews)
	}
}

func TestClientGetsCookieWhenMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("sungero_client"); err != nil || c.Value != "FRESH" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"value":[]}`))
	}))
	defer srv.Close()
	cookie := ""
	cl, err := New(Options{
		BaseURL: srv.URL + "/odata",
		Auth:    "cookie",
		CookieFunc: func() (string, error) {
			if cookie == "" {
				return "", errors.New("нет файла")
			}
			return cookie, nil
		},
		CookieRenew: func(context.Context) error { cookie = "sungero_client=FRESH"; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.List(t.Context(), "IUsers", Query{Top: 1}); err != nil {
		t.Fatalf("без файла куки: %v", err)
	}
}

// Параллельные запросы на истёкшей куке: продление одно, все запросы проходят.
// RX при каждом новом входе гасит предыдущую сессию, поэтому лишние продления
// давали 401 «через раз».
func TestClientRenewsOnceUnderConcurrency(t *testing.T) {
	var mu sync.Mutex
	valid := "S0"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Millisecond) // чтобы запросы действительно пересекались
		mu.Lock()
		ok := false
		if c, err := r.Cookie("sungero_client"); err == nil && c.Value == valid {
			ok = true
		}
		mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"value":[]}`))
	}))
	defer srv.Close()
	var renews atomic.Int64
	cookie := "sungero_client=STALE"
	cl, err := New(Options{
		BaseURL: srv.URL + "/odata",
		Auth:    "cookie",
		CookieFunc: func() (string, error) {
			mu.Lock()
			defer mu.Unlock()
			return cookie, nil
		},
		CookieRenew: func(context.Context) error {
			n := renews.Add(1)
			time.Sleep(20 * time.Millisecond) // вход через провайдера не мгновенный
			mu.Lock()
			valid = fmt.Sprintf("S%d", n)
			cookie = "sungero_client=" + valid
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var failed atomic.Int64
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := cl.List(t.Context(), "IUsers", Query{Top: 1}); err != nil {
				failed.Add(1)
			}
		}()
	}
	wg.Wait()
	if failed.Load() != 0 || renews.Load() != 1 {
		t.Fatalf("неудачных запросов %d, продлений %d (ждали 0 и 1)", failed.Load(), renews.Load())
	}
}
