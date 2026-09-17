package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Demonisreal/dmn-status/internal/config"
	"github.com/Demonisreal/dmn-status/internal/store"
)

// routeEnv hat ein oeffentliches ziel (1), ein verstecktes (2) und eine gueltige sitzung
func routeEnv(t *testing.T) (e *env, pub, hidden int64, sid *http.Cookie, token string) {
	t.Helper()
	e = newEnv(t, config.Config{}, func(st *store.Store) {
		pub = target(t, st, "Website", true)
		hidden = target(t, st, "Intern", false)
	})
	sid, token = e.session()
	return e, pub, hidden, sid, token
}

// untouched prueft, dass keine admin-aktion durchgelaufen ist
func untouched(t *testing.T, e *env, id int64, sid *http.Cookie) {
	t.Helper()
	ctx := context.Background()
	tg, err := e.st.Target(ctx, id)
	if err != nil {
		t.Fatalf("ziel %d weg: %v", id, err)
	}
	if tg.Paused {
		t.Errorf("ziel %d pausiert", id)
	}
	if _, err := e.st.Session(ctx, sid.Value); err != nil {
		t.Errorf("sitzung weg: %v", err)
	}
}

func TestAdminActionsOnlyPost(t *testing.T) {
	e, _, hidden, sid, token := routeEnv(t)
	h := strconv.FormatInt(hidden, 10)
	paths := []string{
		"/admin/ziel/" + h + "/loeschen",
		"/admin/ziel/" + h + "/pause",
		"/admin/ziel/" + h + "/pruefen",
		"/admin/logout",
		"/admin/testmail",
	}
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		for _, p := range paths {
			t.Run(method+p, func(t *testing.T) {
				req := httptest.NewRequest(method, p+"?csrf="+url.QueryEscape(token), strings.NewReader(url.Values{"csrf": {token}}.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.AddCookie(sid)
				rec := e.serve(req)
				if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
					t.Errorf("status %d, Location %q", rec.Code, rec.Header().Get("Location"))
				}
				untouched(t, e, hidden, sid)
			})
		}
	}
}

func TestCSRFOnlyFromBody(t *testing.T) {
	e, _, hidden, sid, token := routeEnv(t)
	req := httptest.NewRequest(http.MethodPost, "/admin/ziel/"+strconv.FormatInt(hidden, 10)+"/loeschen?csrf="+url.QueryEscape(token), nil)
	req.AddCookie(sid)
	if rec := e.serve(req); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/?ok=csrf" {
		t.Errorf("token in der query: status %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
	untouched(t, e, hidden, sid)
}

func TestHeadOnAdminPages(t *testing.T) {
	e, _, hidden, sid, _ := routeEnv(t)
	edit := "/admin/ziel/" + strconv.FormatInt(hidden, 10)

	for _, p := range []string{"/admin/", "/admin/ziel/neu", edit} {
		t.Run("ohne_sitzung"+p, func(t *testing.T) {
			rec := e.serve(httptest.NewRequest(http.MethodHead, p, nil))
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
				t.Errorf("status %d, Location %q", rec.Code, rec.Header().Get("Location"))
			}
			if strings.Contains(rec.Body.String(), "127.0.0.1") {
				t.Error("adresse ohne sitzung ausgeliefert")
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control %q", got)
			}
		})
	}

	req := httptest.NewRequest(http.MethodHead, edit, nil)
	req.AddCookie(sid)
	if rec := e.serve(req); rec.Code != http.StatusOK {
		t.Errorf("HEAD mit sitzung: %d", rec.Code)
	}

	for _, p := range []string{"/admin/", edit} {
		req := httptest.NewRequest(http.MethodOptions, p, nil)
		req.AddCookie(sid)
		rec := e.serve(req)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("OPTIONS %s: status %d", p, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "127.0.0.1") {
			t.Errorf("OPTIONS %s liefert admin-inhalt", p)
		}
	}
}

func TestUncleanPaths(t *testing.T) {
	e, pub, hidden, sid, token := routeEnv(t)
	h := strconv.FormatInt(hidden, 10)

	// der mux leitet mit 307 auf den sauberen pfad um, erst dort greifen sitzung und csrf
	for _, tt := range []struct{ path, location string }{
		{"//admin/", "/admin/"},
		{"/admin//", "/admin/"},
		{"/admin/../admin/", "/admin/"},
		{"/admin/./ziel/" + h, "/admin/ziel/" + h},
		{"//ziel/" + h, "/ziel/" + h},
		{"/ziel//" + strconv.FormatInt(pub, 10), "/ziel/" + strconv.FormatInt(pub, 10)},
	} {
		t.Run("GET"+tt.path, func(t *testing.T) {
			rec := e.get(tt.path)
			if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != tt.location {
				t.Errorf("status %d, Location %q", rec.Code, rec.Header().Get("Location"))
			}
			if strings.Contains(rec.Body.String(), "Intern") || strings.Contains(rec.Body.String(), "127.0.0.1") {
				t.Error("inhalt vor der umleitung")
			}
		})
	}

	for _, p := range []string{"/admin//ziel/" + h + "/loeschen", "//admin/ziel/" + h + "/loeschen", "/admin/ziel/" + h + "//loeschen"} {
		t.Run("POST"+p, func(t *testing.T) {
			rec := e.post(p, url.Values{"csrf": {token}}, sid)
			if rec.Code >= 200 && rec.Code < 300 || rec.Header().Get("Location") == "/admin/?ok=geloescht" {
				t.Errorf("status %d, Location %q", rec.Code, rec.Header().Get("Location"))
			}
			untouched(t, e, hidden, sid)
		})
	}
}

func TestEncodedPaths(t *testing.T) {
	e, pub, hidden, sid, token := routeEnv(t)
	h := strconv.FormatInt(hidden, 10)

	// %31 ist dieselbe url wie 1 (RFC 3986, 6.2.2.2), versteckt bleibt versteckt
	if rec := e.get("/ziel/%" + strconv.FormatInt(0x30+pub, 16)); rec.Code != http.StatusOK {
		t.Errorf("kodierte oeffentliche id: %d", rec.Code)
	}
	for _, p := range []string{
		"/ziel/%" + strconv.FormatInt(0x30+hidden, 16),
		"/en/ziel/%" + strconv.FormatInt(0x30+hidden, 16),
		"/ziel/1%2F",
		"/ziel/%2B1",
		"/ziel/1%20",
		"/ziel/%201",
		"/ziel/1%00",
		"/ziel/%EF%BC%91",
		"/ziel/1%2F..%2F" + h,
		"/ziel/9223372036854775808",
	} {
		t.Run(p, func(t *testing.T) {
			rec := e.get(p)
			if rec.Code != http.StatusNotFound {
				t.Errorf("status %d", rec.Code)
			}
			if strings.Contains(rec.Body.String(), "Intern") {
				t.Error("verstecktes ziel sichtbar")
			}
		})
	}

	// auch kodiert faengt /admin/ die sitzungspruefung und no-store
	rec := e.get("/%61dmin/")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Errorf("/%%61dmin/: status %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("/%%61dmin/: Cache-Control %q", got)
	}

	for _, p := range []string{"/admin/ziel/1%2F..%2F" + h + "/loeschen", "/admin/ziel/%2B" + h + "/loeschen", "/admin/ziel/0" + h + "/loeschen"} {
		t.Run("POST"+p, func(t *testing.T) {
			if rec := e.post(p, url.Values{"csrf": {token}}, sid); rec.Code != http.StatusNotFound {
				t.Errorf("status %d, Location %q", rec.Code, rec.Header().Get("Location"))
			}
			untouched(t, e, hidden, sid)
		})
	}
}

func TestTrailingSlash(t *testing.T) {
	e, pub, hidden, sid, token := routeEnv(t)
	p, h := strconv.FormatInt(pub, 10), strconv.FormatInt(hidden, 10)

	for _, path := range []string{"/ziel/" + p + "/", "/en/ziel/" + p + "/", "/admin/login/", "/api/status.json/", "/healthz/", "/static/app.css/", "/admin/ziel/neu/"} {
		if rec := e.get(path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d", path, rec.Code)
		}
	}
	if rec := e.get("/admin/ziel/"+h+"/", sid); rec.Code != http.StatusNotFound {
		t.Errorf("bearbeiten mit slash: %d", rec.Code)
	}
	if rec := e.post("/admin/ziel/"+h+"/loeschen/", url.Values{"csrf": {token}}, sid); rec.Code != http.StatusNotFound {
		t.Errorf("loeschen mit slash: %d", rec.Code)
	}
	untouched(t, e, hidden, sid)
}

func TestNotFoundLanguage(t *testing.T) {
	e := newEnv(t, config.Config{}, nil)
	if rec := e.get("/en"); rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "/en/" {
		t.Errorf("/en: status %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}

	tests := []struct{ path, lang string }{
		{"/en/gibtsnicht", "en"},
		{"/en/ziel/abc", "en"},
		{"/en/admin/", "en"},
		{"/en/static/app.css", "en"},
		{"/gibtsnicht", "de"},
		{"/english", "de"},
		{"/enx/", "de"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := e.get(tt.path)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status %d", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), `lang="`+tt.lang+`"`) {
				t.Errorf("404 nicht in %s", tt.lang)
			}
		})
	}
}

func TestDeleteUnknownTarget(t *testing.T) {
	e, _, hidden, sid, token := routeEnv(t)
	if rec := e.post("/admin/ziel/999/loeschen", url.Values{"csrf": {token}}, sid); rec.Code != http.StatusNotFound {
		t.Errorf("status %d", rec.Code)
	}
	if _, err := e.st.Target(context.Background(), 999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("ziel 999: %v", err)
	}
	untouched(t, e, hidden, sid)
}
