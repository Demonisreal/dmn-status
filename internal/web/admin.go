package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Demonisreal/dmn-status/internal/auth"
	"github.com/Demonisreal/dmn-status/internal/check"
	"github.com/Demonisreal/dmn-status/internal/store"
)

const maxForm = 64 << 10

// ?ok= waehlt nur aus dieser liste, eigener text laesst sich so nicht einschleusen.
// warn heisst, die aktion ist nicht durchgelaufen.
var flashes = map[string]struct {
	key  string
	warn bool
}{
	"gespeichert": {"adm.saved", false},
	"geloescht":   {"adm.deleted", false},
	"geprueft":    {"adm.checked", false},
	"pausiert":    {"adm.check.paused", true},
	"zufrueh":     {"adm.check.rate", true},
	"mail":        {"adm.mail.sent", false},
	"mailfehler":  {"adm.mail.failed", true},
	"keinmail":    {"adm.mail.off", true},
	"csrf":        {"adm.csrf", true},
}

// mask macht das token in jeder antwort anders (BREACH). Vorne steht das zufallspad, dahinter
// das token xor pad.
func mask(token string) string {
	n := len(token)
	b := make([]byte, 2*n)
	rand.Read(b[:n])
	for i := range n {
		b[n+i] = token[i] ^ b[i]
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func validToken(masked, want string) bool {
	b, err := base64.RawURLEncoding.DecodeString(masked)
	if err != nil || want == "" || len(b) == 0 || len(b)%2 != 0 {
		return false
	}
	n := len(b) / 2
	for i := range n {
		b[n+i] ^= b[i]
	}
	return subtle.ConstantTimeCompare(b[n:], []byte(want)) == 1
}

func (s *server) cookie(name, value string, maxAge int, site http.SameSite) *http.Cookie {
	//nolint:gosec // Secure kommt aus COOKIE_SECURE, standard an, aus nur fuer lokales http
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.Config.CookieSecure,
		SameSite: site,
	}
}

func (s *server) toLogin(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxForm)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Formular ungültig", http.StatusBadRequest)
		return false
	}
	return true
}

// admin prueft die sitzung und bei POST das csrf-token. Der handler bekommt das maskierte
// token fuer die formulare der antwort.
func (s *server) admin(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		c, err := r.Cookie(s.sid)
		if err != nil {
			s.toLogin(w, r)
			return
		}
		sess, err := s.Store.Session(ctx, c.Value)
		if errors.Is(err, store.ErrNotFound) {
			http.SetCookie(w, s.cookie(s.sid, "", -1, http.SameSiteLaxMode))
			s.toLogin(w, r)
			return
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		// sonst schreibt jeder seitenaufruf in die datenbank
		if s.Now().Sub(sess.LastSeen) >= time.Minute {
			if err := s.Store.TouchSession(ctx, c.Value); err != nil && !errors.Is(err, store.ErrNotFound) {
				slog.Warn("sitzung verlaengern", "err", err)
			}
		}

		if r.Method == http.MethodPost {
			if !parseForm(w, r) {
				return
			}
			// meist ein alter tab nach neuem login, die liste zeigt dann den hinweis
			if !validToken(r.PostForm.Get("csrf"), sess.CSRF) {
				http.Redirect(w, r, "/admin/?ok=csrf", http.StatusSeeOther)
				return
			}
		}
		h(w, r, mask(sess.CSRF))
	}
}

func (s *server) adminPage(title, path, csrf string) Page {
	return Page{Title: title, Lang: "de", Path: path, Version: s.Version, CSRF: csrf, Admin: true}
}

func (s *server) loginPage(csrf string) Page {
	return Page{Title: "Anmelden", Lang: "de", Path: "/admin/login", Version: s.Version, CSRF: csrf}
}

func (s *server) loginForm(w http.ResponseWriter, r *http.Request) {
	token := ""
	if c, err := r.Cookie(s.lcsrf); err == nil && len(c.Value) >= 26 && len(c.Value) <= 64 {
		token = c.Value
	} else {
		token = rand.Text()
		http.SetCookie(w, s.cookie(s.lcsrf, token, 0, http.SameSiteStrictMode))
	}
	s.render(w, r, http.StatusOK, "login", LoginPage{Page: s.loginPage(mask(token))})
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if !parseForm(w, r) {
		return
	}
	user, password := r.PostForm.Get("username"), r.PostForm.Get("password")

	// vor dem login gibt es keine sitzung, also double-submit gegen das cookie
	c, err := r.Cookie(s.lcsrf)
	if err != nil || !validToken(r.PostForm.Get("csrf"), c.Value) {
		token := rand.Text()
		http.SetCookie(w, s.cookie(s.lcsrf, token, 0, http.SameSiteStrictMode))
		page := LoginPage{Page: s.loginPage(mask(token)), Username: user, Error: T("de", "adm.csrf")}
		s.render(w, r, http.StatusForbidden, "login", page)
		return
	}
	page := LoginPage{Page: s.loginPage(mask(c.Value)), Username: user}
	ip := s.clientIP(r)

	known := false
	if d, err := r.Cookie(s.dev); err == nil {
		if known, err = s.Store.KnownDevice(r.Context(), d.Value); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	// das limit kommt vor dem semaphor, sonst blockiert eine flut von einer gesperrten ip
	// die argon2-plaetze fuer alle anderen
	if wait, first := s.limit.take(ip, s.Now(), known); wait > 0 {
		if first {
			slog.Warn("login gesperrt", "ip", ip)
		}
		w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
		page.Error = T("de", "adm.login.locked")
		s.render(w, r, http.StatusTooManyRequests, "login", page)
		return
	}

	select {
	case s.verify <- struct{}{}:
		defer func() { <-s.verify }()
	case <-r.Context().Done():
		return
	}

	ok, err := s.checkLogin(r.Context(), user, password)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !ok {
		slog.Warn("login fehlgeschlagen", "ip", ip)
		page.Error = T("de", "adm.login.bad")
		s.render(w, r, http.StatusUnauthorized, "login", page)
		return
	}
	s.limit.forgive(ip)

	if old, err := r.Cookie(s.sid); err == nil {
		if err := s.Store.DeleteSession(r.Context(), old.Value); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	token, _, err := s.Store.CreateSession(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.SetCookie(w, s.cookie(s.sid, token, 0, http.SameSiteLaxMode))
	if !known {
		// das geraete-cookie ist nur komfort, die sitzung steht schon
		if dev, err := s.Store.CreateDevice(r.Context()); err != nil {
			slog.Warn("geraet merken", "err", err)
		} else {
			http.SetCookie(w, s.cookie(s.dev, dev, int(store.DeviceMax.Seconds()), http.SameSiteStrictMode))
		}
	}
	slog.Info("login erfolgreich", "ip", ip)
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

// checkLogin laesst in jedem fall genau einen argon2-lauf laufen, die antwortzeit soll weder
// den benutzernamen noch einen fehlenden admin verraten
func (s *server) checkLogin(ctx context.Context, user, password string) (bool, error) {
	name, hash, err := s.Store.Admin(ctx)
	if errors.Is(err, store.ErrNotFound) {
		auth.DummyVerify(password)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	a, b := sha256.Sum256([]byte(user)), sha256.Sum256([]byte(name))
	nameOK := subtle.ConstantTimeCompare(a[:], b[:]) == 1
	ok, err := auth.Verify(password, hash)
	if err != nil {
		slog.Error("admin-hash", "err", err)
		auth.DummyVerify(password)
		return false, nil
	}
	return nameOK && ok, nil
}

func (s *server) logout(w http.ResponseWriter, r *http.Request, _ string) {
	if c, err := r.Cookie(s.sid); err == nil {
		if err := s.Store.DeleteSession(r.Context(), c.Value); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	http.SetCookie(w, s.cookie(s.sid, "", -1, http.SameSiteLaxMode))
	s.toLogin(w, r)
}

func (s *server) list(w http.ResponseWriter, r *http.Request, csrf string) {
	ctx := r.Context()
	targets, err := s.Store.Targets(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p := AdminListPage{Page: s.adminPage("Ziele", "/admin/", csrf), MailOK: s.Mailer != nil}
	if f, ok := flashes[r.URL.Query().Get("ok")]; ok {
		p.Flash, p.FlashWarn = T("de", f.key), f.warn
	}
	for _, t := range targets {
		row, last, err := s.row(ctx, t, 0, "de")
		if err != nil {
			s.fail(w, r, err)
			return
		}
		a := AdminTargetRow{TargetRow: row, Address: t.Address, Paused: t.Paused, Public: t.Public, LastCheck: last.At}
		if !last.OK {
			a.LastError = last.Err
		}
		p.Targets = append(p.Targets, a)
	}
	s.render(w, r, http.StatusOK, "admin_list", p)
}

func form(t check.Target) TargetForm {
	return TargetForm{
		ID:            t.ID,
		Name:          t.Name,
		Kind:          t.Kind,
		Address:       t.Address,
		ExpectStatus:  t.ExpectStatus,
		Keyword:       t.Keyword,
		ConnectTo:     t.ConnectTo,
		IntervalS:     t.IntervalS,
		TimeoutMs:     t.TimeoutMs,
		FailThreshold: t.FailThreshold,
		Public:        t.Public,
		Paused:        t.Paused,
	}
}

func (s *server) formPage(t check.Target, csrf string) Page {
	if t.ID == 0 {
		return s.adminPage("Ziel anlegen", "/admin/ziel/neu", csrf)
	}
	return s.adminPage("Ziel bearbeiten", "/admin/ziel/"+strconv.FormatInt(t.ID, 10), csrf)
}

func newTarget() check.Target {
	return check.Target{Kind: check.KindHTTP, ExpectStatus: "200-399", IntervalS: 60, TimeoutMs: 10000, FailThreshold: 3}
}

func (s *server) newForm(w http.ResponseWriter, r *http.Request, csrf string) {
	t := newTarget()
	f := form(t)
	f.Page = s.formPage(t, csrf)
	s.render(w, r, http.StatusOK, "admin_form", f)
}

// target laedt das ziel aus dem pfad. false heisst, die antwort ist schon geschrieben.
func (s *server) target(w http.ResponseWriter, r *http.Request) (check.Target, bool) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return check.Target{}, false
	}
	t, err := s.Store.Target(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return t, false
	}
	if err != nil {
		s.fail(w, r, err)
		return t, false
	}
	return t, true
}

func (s *server) editForm(w http.ResponseWriter, r *http.Request, csrf string) {
	t, ok := s.target(w, r)
	if !ok {
		return
	}
	f := form(t)
	f.Page = s.formPage(t, csrf)
	s.render(w, r, http.StatusOK, "admin_form", f)
}

func (s *server) save(w http.ResponseWriter, r *http.Request, csrf string) {
	t := newTarget()
	if r.PathValue("id") != "" {
		var ok bool
		if t, ok = s.target(w, r); !ok {
			return
		}
	}

	f := r.PostForm
	t.Name = strings.TrimSpace(f.Get("name"))
	t.Kind = f.Get("kind")
	t.Address = strings.TrimSpace(f.Get("address"))
	t.ExpectStatus = strings.TrimSpace(f.Get("expect_status"))
	t.Keyword = f.Get("keyword")
	t.ConnectTo = strings.TrimSpace(f.Get("connect_to"))
	t.Public = f.Get("public") == "1"
	t.Paused = f.Get("paused") == "1"

	// eine kaputte zahl laesst den bisherigen wert stehen: das formular zeigt dann keine 0, und
	// die timeout-pruefung rechnet weiter mit einem sinnvollen intervall
	bad := map[string]bool{}
	for key, dst := range map[string]*int{"interval_s": &t.IntervalS, "timeout_ms": &t.TimeoutMs, "fail_threshold": &t.FailThreshold} {
		n, err := strconv.Atoi(strings.TrimSpace(f.Get(key)))
		if err != nil {
			bad[key] = true
			continue
		}
		*dst = n
	}
	errs := check.Validate(t)
	for key := range bad {
		errs[key] = "Ganze Zahl erwartet"
	}
	if len(errs) > 0 {
		page := form(t)
		page.Page = s.formPage(t, csrf)
		page.Errors = errs
		s.render(w, r, http.StatusUnprocessableEntity, "admin_form", page)
		return
	}

	ctx := r.Context()
	var err error
	if t.ID == 0 {
		t.ID, err = s.Store.CreateTarget(ctx, t)
	} else {
		err = s.Store.UpdateTarget(ctx, t)
	}
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.Monitor.Reload(ctx, t.ID); err != nil {
		slog.Error("monitor neu laden", "target", t.ID, "err", err)
	}
	s.clearCache()
	http.Redirect(w, r, "/admin/?ok=gespeichert", http.StatusSeeOther)
}

func (s *server) pause(w http.ResponseWriter, r *http.Request, _ string) {
	t, ok := s.target(w, r)
	if !ok {
		return
	}
	err := s.Store.SetPaused(r.Context(), t.ID, !t.Paused)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.Monitor.Reload(r.Context(), t.ID); err != nil {
		slog.Error("monitor neu laden", "target", t.ID, "err", err)
	}
	s.clearCache()
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (s *server) checkNow(w http.ResponseWriter, r *http.Request, _ string) {
	t, ok := s.target(w, r)
	if !ok {
		return
	}
	flash := "geprueft"
	switch {
	case t.Paused:
		flash = "pausiert"
	case !s.Monitor.CheckNow(t.ID):
		flash = "zufrueh"
	}
	http.Redirect(w, r, "/admin/?ok="+flash, http.StatusSeeOther)
}

func (s *server) remove(w http.ResponseWriter, r *http.Request, _ string) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	err := s.Monitor.Delete(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.clearCache()
	http.Redirect(w, r, "/admin/?ok=geloescht", http.StatusSeeOther)
}

func (s *server) testMail(w http.ResponseWriter, r *http.Request, _ string) {
	flash := "mail"
	if s.Mailer == nil {
		flash = "keinmail"
	} else {
		// unter dem WriteTimeout des servers bleiben, sonst sieht der browser nur einen abbruch
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if err := s.Mailer.Test(ctx); err != nil {
			slog.Warn("testmail", "err", err)
			flash = "mailfehler"
		}
	}
	http.Redirect(w, r, "/admin/?ok="+flash, http.StatusSeeOther)
}
