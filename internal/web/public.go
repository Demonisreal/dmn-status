package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Demonisreal/dmn-status/internal/check"
	"github.com/Demonisreal/dmn-status/internal/store"
)

// row baut die zeile eines ziels. Zustand, spieler und antwortzeit kommen aus dem letzten
// check in der datenbank, der snapshot des monitors kennt keine spielerzahl.
func (s *server) row(ctx context.Context, t check.Target, hours int, lang string) (TargetRow, store.Check, error) {
	r := TargetRow{ID: t.ID, Name: t.Name, Kind: t.Kind, State: StateUnknown}
	var last store.Check
	checks, err := s.Store.LastChecks(ctx, t.ID, 1)
	if err != nil {
		return r, last, err
	}
	if len(checks) > 0 {
		last = checks[0]
		r.State = StateDown
		if last.OK {
			r.State, r.LatencyMs = StateUp, last.LatencyMs
		}
		if t.Kind == check.KindFiveM && last.Players != nil && last.MaxPlayers != nil {
			r.Players = &Players{Count: *last.Players, Max: *last.MaxPlayers, Version: last.Version}
		}
	}
	if t.Paused {
		r.State = StatePaused
	}

	up, err := s.Store.Uptime(ctx, t.ID)
	if err != nil {
		return r, last, err
	}
	r.Uptime24h, r.Uptime7d, r.Uptime30d = up.Day, up.Week, up.Month

	if hours > 0 {
		h, err := s.Store.Hours(ctx, t.ID, hours)
		if err != nil {
			return r, last, err
		}
		r.Hours = cells(h, 1)
		r.Spark = HourBars(r.Hours, time.Hour, lang)
	}
	return r, last, nil
}

// cells fasst je n stunden zu einer zelle zusammen, fuer 30 tage n = 24
func cells(hours []store.Hour, n int) []HourCell {
	out := make([]HourCell, 0, len(hours)/n)
	for i := 0; i+n <= len(hours); i += n {
		c := HourCell{Start: hours[i].Start, State: StateUnknown, Uptime: -1}
		var total, ok int
		for _, h := range hours[i : i+n] {
			total += h.Total
			ok += h.OK
		}
		if total > 0 {
			c.Uptime = float64(ok) / float64(total)
			c.State = StateUp
			if ok < total {
				c.State = StateDown
			}
		}
		out = append(out, c)
	}
	return out
}

func overallState(rows []TargetRow) State {
	active, seen := 0, false
	for _, r := range rows {
		switch r.State {
		case StateDown:
			return StateDown
		case StateUp:
			seen = true
		}
		if r.State != StatePaused {
			active++
		}
	}
	switch {
	case len(rows) > 0 && active == 0:
		return StatePaused
	case !seen:
		return StateUnknown
	}
	return StateUp
}

func incidentRows(list []store.Incident, now time.Time, admin bool) []IncidentRow {
	out := make([]IncidentRow, 0, len(list))
	for _, i := range list {
		row := IncidentRow{TargetID: i.TargetID, TargetName: i.TargetName, StartedAt: i.StartedAt, Duration: now.Sub(i.StartedAt)}
		if !i.Open() {
			end := i.EndedAt
			row.EndedAt, row.Duration = &end, end.Sub(i.StartedAt)
		}
		if admin {
			row.Cause = i.Cause
		}
		out = append(out, row)
	}
	return out
}

func statusPath(lang string) string {
	return base(lang) + "/"
}

func detailPath(lang string, id int64, rng string) string {
	p := base(lang) + "/ziel/" + strconv.FormatInt(id, 10)
	if rng != "24h" {
		p += "?r=" + rng
	}
	return p
}

func other(lang string) string {
	if lang == "en" {
		return "de"
	}
	return "en"
}

const cacheTTL = 5 * time.Second

type cached struct {
	mu   sync.Mutex
	body []byte
	at   time.Time
}

// defer, weil eine panic im template den eintrag sonst fuer immer sperrt
func (c *cached) get(now func() time.Time, build func() ([]byte, error)) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t := now(); c.body == nil || t.Sub(c.at) >= cacheTTL {
		body, err := build()
		if err != nil {
			return nil, err
		}
		c.body, c.at = body, t
	}
	return c.body, nil
}

// fromCache liefert fuer key hoechstens cacheTTL alte bytes aus. Kommen viele aufrufe
// gleichzeitig, rechnet nur einer, die anderen warten am mutex des eintrags. Fehler landen
// nicht im cache.
func (s *server) fromCache(w http.ResponseWriter, r *http.Request, key, ctype string, build func() ([]byte, error)) {
	s.cacheMu.Lock()
	c := s.cache[key]
	if c == nil {
		c = &cached{}
		s.cache[key] = c
	}
	s.cacheMu.Unlock()

	body, err := c.get(s.Now, build)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.Write(body)
}

// clearCache wirft alle eintraege weg. Nach einer aenderung an den zielen soll ein gerade
// intern gestelltes ziel nicht noch bis zu cacheTTL oeffentlich zu sehen sein.
func (s *server) clearCache() {
	s.cacheMu.Lock()
	clear(s.cache)
	s.cacheMu.Unlock()
}

func (s *server) status(w http.ResponseWriter, r *http.Request, lang string) {
	s.fromCache(w, r, "status/"+lang, "text/html; charset=utf-8", func() ([]byte, error) {
		// die berechnung teilen sich alle wartenden, ein abbruch des ersten clients darf sie
		// nicht fuer die anderen scheitern lassen
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
		defer cancel()
		targets, err := s.Store.PublicTargets(ctx)
		if err != nil {
			return nil, err
		}
		p := StatusPage{Page: Page{Title: "Status", Lang: lang, AltURL: statusPath(other(lang)), Path: statusPath(lang), Version: s.Version}}
		for _, t := range targets {
			row, last, err := s.row(ctx, t, 90, lang)
			if err != nil {
				return nil, err
			}
			if last.At.After(p.CheckedAt) {
				p.CheckedAt = last.At
			}
			p.Targets = append(p.Targets, row)
		}
		p.Overall = overallState(p.Targets)

		now := s.Now()
		inc, err := s.Store.RecentIncidents(ctx, 20, now.Add(-30*24*time.Hour), true)
		if err != nil {
			return nil, err
		}
		p.Incidents = incidentRows(inc, now, false)
		return execute("status", p)
	})
}

type period struct {
	dur   time.Duration
	hours int // stunden fuer die leiste
	group int // stunden je segment
	step  time.Duration
	slots int // punkte der antwortzeit, passend zu den schritten in store.Latency
}

var periods = map[string]period{
	"24h": {24 * time.Hour, 24, 1, time.Hour, 144},
	"7d":  {7 * 24 * time.Hour, 168, 1, time.Hour, 168},
	"30d": {30 * 24 * time.Hour, 720, 24, 24 * time.Hour, 720},
}

func (s *server) detail(w http.ResponseWriter, r *http.Request, lang string) {
	id, ok := pathID(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	ctx := r.Context()
	t, err := s.Store.Target(ctx, id)
	if errors.Is(err, store.ErrNotFound) || err == nil && !t.Public {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}

	rng := r.URL.Query().Get("r")
	sp, ok := periods[rng]
	if !ok {
		rng, sp = "24h", periods["24h"]
	}

	row, _, err := s.row(ctx, t, 0, lang)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	hours, err := s.Store.Hours(ctx, id, sp.hours)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	row.Hours = cells(hours, sp.group)
	row.Spark = HourBars(row.Hours, sp.step, lang)

	points, err := s.Store.Latency(ctx, id, sp.dur)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	now := s.Now()
	inc, err := s.Store.TargetIncidents(ctx, id, 50, now.Add(-sp.dur))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.render(w, r, http.StatusOK, "detail", DetailPage{
		Page:      Page{Title: t.Name, Lang: lang, AltURL: detailPath(other(lang), id, rng), Path: detailPath(lang, id, rng), Version: s.Version},
		Target:    row,
		Range:     rng,
		Latency:   LatencyLine(slots(points, now, sp.dur, sp.slots), lang),
		Incidents: incidentRows(inc, now, false),
	})
}

// slots verteilt die punkte auf feste schritte ueber den zeitraum, schritte ohne messung
// bleiben -1 und werden im diagramm zur luecke
func slots(points []store.Point, now time.Time, dur time.Duration, n int) []int {
	step := dur / time.Duration(n)
	first := now.Truncate(step).Add(-time.Duration(n-1) * step)
	ms := make([]int, n)
	for i := range ms {
		ms[i] = -1
	}
	for _, p := range points {
		if i := int(p.At.Sub(first) / step); p.At.Compare(first) >= 0 && i < n {
			ms[i] = p.Avg
		}
	}
	return ms
}

type apiTarget struct {
	Name      string              `json:"name"`
	Kind      string              `json:"kind"`
	State     State               `json:"state"`
	Uptime    map[string]*float64 `json:"uptime"`
	CheckedAt *time.Time          `json:"checked_at"`
}

func ratio(v float64) *float64 {
	if v < 0 {
		return nil
	}
	return &v
}

func (s *server) api(w http.ResponseWriter, r *http.Request) {
	s.fromCache(w, r, "api", "application/json", func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
		defer cancel()
		targets, err := s.Store.PublicTargets(ctx)
		if err != nil {
			return nil, err
		}
		out := struct {
			Targets []apiTarget `json:"targets"`
		}{Targets: []apiTarget{}}
		for _, t := range targets {
			row, last, err := s.row(ctx, t, 0, "en")
			if err != nil {
				return nil, err
			}
			a := apiTarget{
				Name:   row.Name,
				Kind:   row.Kind,
				State:  row.State,
				Uptime: map[string]*float64{"24h": ratio(row.Uptime24h), "7d": ratio(row.Uptime7d), "30d": ratio(row.Uptime30d)},
			}
			if !last.At.IsZero() {
				a.CheckedAt = &last.At
			}
			out.Targets = append(out.Targets, a)
		}
		return json.Marshal(out)
	})
}

var labelEscape = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func (s *server) metrics(w http.ResponseWriter, r *http.Request) {
	// ueber den hash vergleichen, ConstantTimeCompare verraet sonst die laenge des tokens
	got, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(s.Config.MetricsToken))
	if !found || got == "" || subtle.ConstantTimeCompare(a[:], b[:]) != 1 {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "nicht angemeldet", http.StatusUnauthorized)
		return
	}

	targets, err := s.Store.Targets(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	snap := s.Monitor.Snapshot()

	var up, lat, total strings.Builder
	up.WriteString("# HELP dmn_status_up Ergebnis des letzten Checks, 1 erreichbar.\n# TYPE dmn_status_up gauge\n")
	lat.WriteString("# HELP dmn_status_latency_seconds Antwortzeit des letzten Checks.\n# TYPE dmn_status_latency_seconds gauge\n")
	total.WriteString("# HELP dmn_status_checks_total Checks seit dem Start des Prozesses.\n# TYPE dmn_status_checks_total counter\n")
	for _, t := range targets {
		st, ok := snap[t.ID]
		if !ok || !st.Checked {
			continue
		}
		name := labelEscape.Replace(t.Name)
		v := 0
		if st.OK {
			v = 1
		}
		fmt.Fprintf(&up, "dmn_status_up{target=\"%s\",kind=\"%s\"} %d\n", name, labelEscape.Replace(t.Kind), v)
		fmt.Fprintf(&lat, "dmn_status_latency_seconds{target=\"%s\"} %s\n", name, strconv.FormatFloat(float64(st.LatencyMs)/1000, 'f', -1, 64))
		fmt.Fprintf(&total, "dmn_status_checks_total{target=\"%s\",result=\"ok\"} %d\n", name, st.ChecksOK)
		fmt.Fprintf(&total, "dmn_status_checks_total{target=\"%s\",result=\"fail\"} %d\n", name, st.ChecksFail)
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "%s%s%s# HELP dmn_status_build_info Version des laufenden Builds.\n# TYPE dmn_status_build_info gauge\ndmn_status_build_info{version=\"%s\"} 1\n",
		up.String(), lat.String(), total.String(), labelEscape.Replace(s.Version))
}
