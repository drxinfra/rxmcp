package weblogin

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// стенд: RX с входом через провайдера и провайдер с живой сессией KEYCLOAK_IDENTITY=good.
func stand(t *testing.T) (rx, sso *httptest.Server) {
	t.Helper()
	sso = httptest.NewTLSServer(nil)
	rx = httptest.NewTLSServer(nil)
	t.Cleanup(rx.Close)
	t.Cleanup(sso.Close)
	sso.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/realms/test/protocol/openid-connect/auth" {
			http.NotFound(w, r)
			return
		}
		c, err := r.Cookie("KEYCLOAK_IDENTITY")
		if err != nil || (c.Value != "good" && c.Value != "good2") {
			fmt.Fprint(w, `<html><form id="kc-form-login" action="/login-actions/authenticate"><input name="username"><input type="password" name="password"></form></html>`)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "KEYCLOAK_IDENTITY", Value: "good2", Path: "/realms/test/", Secure: true, HttpOnly: true})
		fmt.Fprintf(w, `<HTML><BODY Onload="document.forms[0].submit()"><FORM METHOD="POST" ACTION="%s">`+
			`<INPUT TYPE="HIDDEN" NAME="code" VALUE="abc" /><INPUT TYPE="HIDDEN" NAME="state" VALUE="s&amp;1" />`+
			`<NOSCRIPT><INPUT name="continue" TYPE="SUBMIT" VALUE="CONTINUE" /></NOSCRIPT></FORM></BODY></HTML>`,
			r.URL.Query().Get("redirect_uri"))
	})
	rx.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Client/login":
			if _, err := r.Cookie("external_identity"); err == nil {
				http.Redirect(w, r, "/Client/", http.StatusFound)
				return
			}
			if r.Header.Get("Sec-Fetch-Mode") != "navigate" {
				fmt.Fprint(w, "<html>spa</html>")
				return
			}
			q := url.Values{"client_id": {"rx"}, "redirect_uri": {rx.URL + "/Client/b1"}, "response_mode": {"form_post"}}
			http.Redirect(w, r, sso.URL+"/realms/test/protocol/openid-connect/auth?"+q.Encode(), http.StatusFound)
		case "/Client/b1":
			if r.Method != http.MethodPost || r.FormValue("code") != "abc" || r.FormValue("state") != "s&1" {
				http.Error(w, "bad code", http.StatusBadRequest)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "external_identity", Value: "ext", Path: "/", Secure: true})
			http.Redirect(w, r, "/Client/login", http.StatusFound)
		case "/Client/":
			fmt.Fprint(w, "<html>spa</html>")
		case "/Client/api/login.auto-logon-with-two-factor":
			if _, err := r.Cookie("external_identity"); err != nil || r.Header.Get("X-Client-Id") == "" {
				http.Error(w, "no identity", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "sungero_client", Value: "S1", Path: "/", Secure: true})
			fmt.Fprint(w, `{"ClientId":"x","Challenge":null}`)
		default:
			http.NotFound(w, r)
		}
	})
	return rx, sso
}

func TestDiscoverAndLogin(t *testing.T) {
	rx, sso := stand(t)
	tr := rx.Client().Transport
	ctx := t.Context()

	scope, err := Discover(ctx, tr, rx.URL)
	if err != nil {
		t.Fatal(err)
	}
	if scope != sso.URL+"/realms/test/" {
		t.Fatalf("область провайдера %q", scope)
	}

	st := &State{URL: scope, Cookies: "KEYCLOAK_IDENTITY=good; KEYCLOAK_SESSION=sess"}
	ck, err := Login(ctx, tr, rx.URL, st)
	if err != nil {
		t.Fatal(err)
	}
	if ck != "sungero_client=S1" {
		t.Fatalf("кука RX %q", ck)
	}
	if !strings.Contains(st.Cookies, "KEYCLOAK_IDENTITY=good2") || st.ClientID == "" {
		t.Fatalf("куки провайдера не обновлены: %q, client_id %q", st.Cookies, st.ClientID)
	}

	// Повторный вход с перевыпущенной кукой провайдера тоже проходит.
	if _, err := Login(ctx, tr, rx.URL, st); err != nil {
		t.Fatalf("второй вход: %v", err)
	}
}

func TestLoginExpiredSession(t *testing.T) {
	rx, sso := stand(t)
	st := &State{URL: sso.URL + "/realms/test/", Cookies: "KEYCLOAK_IDENTITY=old"}
	if _, err := Login(t.Context(), rx.Client().Transport, rx.URL, st); !errors.Is(err, ErrNoSession) {
		t.Fatalf("ждали ErrNoSession, получили %v", err)
	}
}

func TestRenewerSavesBoth(t *testing.T) {
	rx, sso := stand(t)
	sf := filepath.Join(t.TempDir(), "sso.json")
	if err := Save(sf, &State{URL: sso.URL + "/realms/test/", Cookies: "KEYCLOAK_IDENTITY=good"}); err != nil {
		t.Fatal(err)
	}
	var saved string
	r := &Renewer{Root: rx.URL, Transport: rx.Client().Transport, StateFile: sf, Save: func(s string) error { saved = s; return nil }}
	if err := r.Renew(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, err := Load(sf)
	if err != nil {
		t.Fatal(err)
	}
	if saved != "sungero_client=S1" || !strings.Contains(st.Cookies, "good2") {
		t.Fatalf("кука RX %q, провайдер %q", saved, st.Cookies)
	}
	// Второй 401 сразу после входа не гоняет провайдера по кругу.
	if err := r.Renew(t.Context()); err == nil {
		t.Fatal("повторный вход через секунду должен отказать")
	}
}

func TestParseProviderCookies(t *testing.T) {
	if _, err := ParseProviderCookies("sungero_client=x; external_identity=y"); err == nil {
		t.Fatal("куки RX вместо провайдера должны отвергаться")
	}
	got, err := ParseProviderCookies("Cookie: AUTH_SESSION_ID=a; KEYCLOAK_IDENTITY=b.c.d; KEYCLOAK_SESSION=e")
	if err != nil || got != "AUTH_SESSION_ID=a; KEYCLOAK_IDENTITY=b.c.d; KEYCLOAK_SESSION=e" {
		t.Fatalf("%q, %v", got, err)
	}
}
