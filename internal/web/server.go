package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"path"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Demonisreal/dmn-status/internal/alert"
	"github.com/Demonisreal/dmn-status/internal/config"
	"github.com/Demonisreal/dmn-status/internal/monitor"
	"github.com/Demonisreal/dmn-status/internal/store"
)

type Deps struct {
	Store   *store.Store
	Monitor *monitor.Manager
	Mailer  *alert.Mailer // nil ohne smtp
	Config  config.Config
	Version string
	Now     func() time.Time
}

const csp = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; " +
	"form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

type server struct {
	Deps
	sid, lcsrf, dev string

	// argon2 braucht 19 MiB je lauf, mehr als zwei gleichzeitig vertraegt der container nicht
	verify chan struct{}
	limit  *limiter

	cacheMu sync.Mutex
	cache   map[string]*cached
}

func New(d Deps) http.Handler {
	return newServer(d).handler()
}

func newServer(d Deps) *server {
	s := &server{
		Deps:   d,
		sid:    "sid",
		lcsrf:  "lcsrf",
		dev:    "dev",
		verify: make(chan struct{}, 2),
		limit:  newLimiter(),
		cache:  map[string]*cached{},
	}
	// __Host- verlangt Secure, ohne https wuerde der browser das cookie verwerfen
	if d.Config.CookieSecure {
		s.sid, s.lcsrf, s.dev = "__Host-sid", "__Host-lcsrf", "__Host-dev"
	}
	return s
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { s.status(w, r, "de") })
	mux.HandleFunc("GET /en/{$}", func(w http.ResponseWriter, r *http.Request) { s.status(w, r, "en") })
	mux.HandleFunc("GET /ziel/{id}", func(w http.ResponseWriter, r *http.Request) { s.detail(w, r, "de") })
	mux.HandleFunc("GET /en/ziel/{id}", func(w http.ResponseWriter, r *http.Request) { s.detail(w, r, "en") })
	mux.HandleFunc("GET /api/status.json", s.api)
	mux.HandleFunc("GET /healthz", s.healthz)
	if s.Config.MetricsToken != "" {
		mux.HandleFunc("GET /metrics", s.metrics)
	}
	mux.HandleFunc("GET /static/{file...}", s.static)

	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /admin/login", s.loginForm)
	mux.HandleFunc("POST /admin/login", s.login)
	mux.HandleFunc("POST /admin/logout", s.admin(s.logout))
	mux.HandleFunc("GET /admin/{$}", s.admin(s.list))
	mux.HandleFunc("GET /admin/ziel/neu", s.admin(s.newForm))
	mux.HandleFunc("POST /admin/ziel/neu", s.admin(s.save))
	mux.HandleFunc("GET /admin/ziel/{id}", s.admin(s.editForm))
	mux.HandleFunc("POST /admin/ziel/{id}", s.admin(s.save))
	mux.HandleFunc("POST /admin/ziel/{id}/pause", s.admin(s.pause))
	mux.HandleFunc("POST /admin/ziel/{id}/pruefen", s.admin(s.checkNow))
	mux.HandleFunc("POST /admin/ziel/{id}/loeschen", s.admin(s.remove))
	mux.HandleFunc("POST /admin/testmail", s.admin(s.testMail))

	mux.HandleFunc("/", s.notFound)

	return s.headers(s.recover(http.NewCrossOriginProtection().Handler(mux)))
}

func (s *server) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")

		p := r.URL.Path
		switch {
		case p == "/admin" || strings.HasPrefix(p, "/admin/"), p == "/metrics", p == "/healthz":
			h.Set("Cache-Control", "no-store")
		case strings.HasPrefix(p, "/static/"):
			// setzt static selbst, je nach ?v=
		default:
			h.Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

type tracked struct {
	http.ResponseWriter
	wrote bool
}

func (t *tracked) WriteHeader(code int) {
	t.wrote = true
	t.ResponseWriter.WriteHeader(code)
}

func (t *tracked) Write(b []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriter.Write(b)
}

func (t *tracked) Unwrap() http.ResponseWriter { return t.ResponseWriter }

func (s *server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tw := &tracked{ResponseWriter: w}
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler {
				panic(v)
			}
			slog.Error("panic", "method", r.Method, "path", r.URL.Path, "err", v, "stack", string(debug.Stack()))
			if !tw.wrote {
				s.errorPage(w, r)
			}
		}()
		next.ServeHTTP(tw, r)
	})
}

func lang(r *http.Request) string {
	if r.URL.Path == "/en" || strings.HasPrefix(r.URL.Path, "/en/") {
		return "en"
	}
	return "de"
}

func (s *server) notFound(w http.ResponseWriter, r *http.Request) {
	l := lang(r)
	p := Page{Title: T(l, "nf.title"), Lang: l, Version: s.Version}
	if err := Render(w, http.StatusNotFound, "notfound", p); err != nil {
		slog.Error("404 rendern", "err", err)
	}
}

func (s *server) errorPage(w http.ResponseWriter, r *http.Request) {
	l := lang(r)
	p := Page{Title: T(l, "err.title"), Lang: l, Version: s.Version}
	if err := Render(w, http.StatusInternalServerError, "error", p); err != nil {
		slog.Error("500 rendern", "err", err)
		http.Error(w, "interner fehler", http.StatusInternalServerError)
	}
}

// fail loggt den fehler und zeigt die 500-seite. Der client sieht nie err.Error().
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		return
	}
	slog.Error("anfrage", "method", r.Method, "path", r.URL.Path, "err", err)
	s.errorPage(w, r)
}

func (s *server) render(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	if err := Render(w, status, name, data); err != nil {
		s.fail(w, r, err)
	}
}

// pathID akzeptiert nur die kanonische schreibweise, "+7" oder "007" waeren sonst dieselbe seite
func pathID(r *http.Request) (int64, bool) {
	v := r.PathValue("id")
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != v {
		return 0, false
	}
	return id, true
}

func (s *server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err := s.Store.Ping(ctx); err != nil {
		slog.Error("healthz", "err", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("db nicht erreichbar\n"))
		return
	}
	w.Write([]byte("ok\n"))
}

var staticTypes = map[string]string{
	".css": "text/css; charset=utf-8",
	".js":  "text/javascript; charset=utf-8",
	".svg": "image/svg+xml",
}

// Die mime-tabelle kommt unter windows teils aus der registry, dort ist .js gern text/plain.
// Mit nosniff laedt der browser das skript dann nicht, deshalb die feste liste.
func (s *server) static(w http.ResponseWriter, r *http.Request) {
	// headers laesst /static/ aus, ohne das haette auch die 404 keinen Cache-Control
	h := w.Header()
	h.Set("Cache-Control", "no-cache")

	name := r.PathValue("file")
	ct, ok := staticTypes[path.Ext(name)]
	if !ok {
		s.notFound(w, r)
		return
	}
	st, err := staticFS.Open(name)
	if err != nil {
		s.notFound(w, r)
		return
	}
	info, err := st.Stat()
	st.Close()
	if err != nil || info.IsDir() {
		s.notFound(w, r)
		return
	}

	h.Set("Content-Type", ct)
	if r.URL.Query().Has("v") {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeFileFS(w, r, staticFS, name) //nolint:gosec // embed-fs, Open oben prueft name schon per fs.ValidPath
}

var staticFS = Static()
