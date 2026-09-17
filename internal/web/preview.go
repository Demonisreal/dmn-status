//go:build dev

package web

import (
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Preview liefert alle seiten mit festen beispieldaten, nur fuer die arbeit an der oberflaeche.
// Die zahlen sind ausgedacht und duerfen nie in einem echten build landen.
func Preview() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(Static())))

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { show(w, "status", demoStatus("de", r.URL.Path)) })
	mux.HandleFunc("GET /en/{$}", func(w http.ResponseWriter, r *http.Request) { show(w, "status", demoStatus("en", r.URL.Path)) })

	// randfaelle der statusseite
	mux.HandleFunc("GET /leer", func(w http.ResponseWriter, r *http.Request) {
		p := demoStatus("de", r.URL.Path)
		p.Targets, p.Incidents, p.Overall = nil, nil, StateUnknown
		show(w, "status", p)
	})
	mux.HandleFunc("GET /neu", func(w http.ResponseWriter, r *http.Request) {
		p := demoStatus("de", r.URL.Path)
		for i := range p.Targets {
			t := &p.Targets[i]
			t.State, t.Uptime24h, t.Uptime7d, t.Uptime30d, t.LatencyMs, t.Players = StateUnknown, -1, -1, -1, 0, nil
			t.Hours = demoHours(90, time.Hour, func(int) State { return StateUnknown })
			t.Spark = HourBars(t.Hours, time.Hour, "de")
		}
		p.Overall, p.Incidents, p.CheckedAt = StateUnknown, nil, time.Time{}
		show(w, "status", p)
	})
	mux.HandleFunc("GET /ruhig", func(w http.ResponseWriter, r *http.Request) {
		p := demoStatus("de", r.URL.Path)
		p.Targets = p.Targets[:2]
		p.Overall, p.Incidents = StateUp, nil
		show(w, "status", p)
	})

	mux.HandleFunc("GET /ziel/{id}", func(w http.ResponseWriter, r *http.Request) { detail(w, r, "de") })
	mux.HandleFunc("GET /en/ziel/{id}", func(w http.ResponseWriter, r *http.Request) { detail(w, r, "en") })

	mux.HandleFunc("GET /admin/{$}", func(w http.ResponseWriter, r *http.Request) {
		p := demoAdmin()
		p.Flash = T("de", "adm.saved")
		show(w, "admin_list", p)
	})
	mux.HandleFunc("GET /admin/leer", func(w http.ResponseWriter, r *http.Request) {
		p := demoAdmin()
		p.Targets, p.MailOK = nil, false
		show(w, "admin_list", p)
	})
	mux.HandleFunc("GET /admin/ziel/neu", func(w http.ResponseWriter, r *http.Request) {
		show(w, "admin_form", TargetForm{Page: adminPage("Ziel anlegen", r.URL.Path), Kind: "http", ExpectStatus: "200-399", IntervalS: 60, TimeoutMs: 10000, FailThreshold: 3})
	})
	mux.HandleFunc("GET /admin/ziel/fehler", func(w http.ResponseWriter, r *http.Request) {
		show(w, "admin_form", TargetForm{
			Page: adminPage("Ziel anlegen", r.URL.Path), Kind: "tcp", Name: "", Address: "localhost", Keyword: "ok",
			ExpectStatus: "200-399", IntervalS: 10, TimeoutMs: 10000, FailThreshold: 3,
			Errors: map[string]string{
				"name":       "1 bis 60 Zeichen",
				"address":    "Adresse als host:port erwartet",
				"keyword":    "Nur bei HTTP",
				"interval_s": "Liegt außerhalb von 30 bis 86400",
			},
		})
	})
	mux.HandleFunc("GET /admin/ziel/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		for _, t := range demoAdmin().Targets {
			if t.ID == id {
				show(w, "admin_form", TargetForm{
					Page: adminPage("Ziel bearbeiten", r.URL.Path), ID: t.ID, Name: t.Name, Kind: t.Kind, Address: t.Address,
					ExpectStatus: "200-399", IntervalS: 60, TimeoutMs: 10000, FailThreshold: 3, Public: t.Public, Paused: t.Paused,
				})
				return
			}
		}
		notFound(w, r)
	})

	mux.HandleFunc("GET /admin/login", func(w http.ResponseWriter, r *http.Request) {
		p := LoginPage{Page: Page{Title: "Anmelden", Lang: "de", Path: r.URL.Path, Version: "dev", CSRF: "vorschau"}}
		status := http.StatusOK
		switch r.URL.Query().Get("fehler") {
		case "1":
			p.Username, p.Error, status = "leon", T("de", "adm.login.bad"), http.StatusUnauthorized
		case "429":
			p.Username, p.Error, status = "leon", T("de", "adm.login.locked"), http.StatusTooManyRequests
		}
		if err := Render(w, status, "login", p); err != nil {
			log.Print(err)
		}
	})

	mux.HandleFunc("/", notFound)
	return mux
}

func show(w http.ResponseWriter, name string, data any) {
	if err := Render(w, http.StatusOK, name, data); err != nil {
		log.Print(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func notFound(w http.ResponseWriter, r *http.Request) {
	p := Page{Title: T("de", "nf.title"), Lang: "de", Path: r.URL.Path, Version: "dev"}
	if strings.HasPrefix(r.URL.Path, "/en/") {
		p.Lang, p.Title = "en", T("en", "nf.title")
	}
	if err := Render(w, http.StatusNotFound, "notfound", p); err != nil {
		log.Print(err)
	}
}

func detail(w http.ResponseWriter, r *http.Request, lang string) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	p := demoStatus(lang, "")
	for _, t := range p.Targets {
		if t.ID != id {
			continue
		}
		rng := r.URL.Query().Get("r")
		cells, step, points := 24, time.Hour, 96
		switch rng {
		case "7d":
			cells, points = 168, 168
		case "30d":
			cells, step, points = 30, 24*time.Hour, 180
		default:
			rng = "24h"
		}

		down := demoDown(t.ID)
		t.Spark = HourBars(demoHours(cells, step, func(i int) State {
			switch {
			case t.State == StateUnknown:
				return StateUnknown
			case t.State == StatePaused && i > cells-6:
				return StateUnknown
			case down(i, cells):
				return StateDown
			}
			return StateUp
		}), step, lang)

		ms := make([]int, points)
		for i := range ms {
			ms[i] = demoMs(t.ID, i)
			if t.State == StateUnknown || (t.ID == 2 && i > points-4) {
				ms[i] = -1
			}
		}

		var inc []IncidentRow
		for _, in := range p.Incidents {
			if in.TargetID == id {
				inc = append(inc, in)
			}
		}

		alt := "/en/ziel/" + r.PathValue("id")
		if lang == "en" {
			alt = "/ziel/" + r.PathValue("id")
		}
		show(w, "detail", DetailPage{
			Page:      Page{Title: t.Name, Lang: lang, AltURL: alt, Path: r.URL.Path, Version: "dev"},
			Target:    t,
			Range:     rng,
			Latency:   LatencyLine(ms, lang),
			Incidents: inc,
		})
		return
	}
	notFound(w, r)
}

func demoStatus(lang, path string) StatusPage {
	now := time.Now()
	alt := "/en/"
	if lang == "en" {
		alt = "/"
	}

	rows := []TargetRow{
		{ID: 1, Name: "Website", Kind: "http", State: StateUp, Uptime24h: 1, Uptime7d: .99982, Uptime30d: .99951, LatencyMs: 142},
		{ID: 2, Name: "Bewerbungsportal", Kind: "http", State: StateDown, Uptime24h: .9583, Uptime7d: .9917, Uptime30d: .99702, LatencyMs: 0},
		{ID: 3, Name: "API", Kind: "tcp", State: StateUp, Uptime24h: 1, Uptime7d: 1, Uptime30d: .99993, LatencyMs: 38},
		{ID: 4, Name: "FiveM Testserver", Kind: "fivem", State: StateUp, Uptime24h: .99305, Uptime7d: .9981, Uptime30d: .9964, LatencyMs: 211,
			Players: &Players{Count: 12, Max: 64, Version: "7290"}},
		{ID: 5, Name: "Archiv der Bewerbungsunterlagen (Backup)", Kind: "http", State: StatePaused, Uptime24h: -1, Uptime7d: .9876, Uptime30d: .9932},
		{ID: 6, Name: "Neues Ziel", Kind: "tcp", State: StateUnknown, Uptime24h: -1, Uptime7d: -1, Uptime30d: -1},
	}
	for i := range rows {
		t := &rows[i]
		down := demoDown(t.ID)
		t.Hours = demoHours(90, time.Hour, func(h int) State {
			switch {
			case t.State == StateUnknown:
				return StateUnknown
			case t.State == StatePaused && h > 70:
				return StateUnknown
			case t.ID == 4 && h > 30 && h < 34:
				return StateUnknown
			case down(h, 90):
				return StateDown
			}
			return StateUp
		})
		t.Spark = HourBars(t.Hours, time.Hour, lang)
	}

	ended := now.Add(-26*time.Hour - 12*time.Minute)
	ended2 := now.Add(-9*24*time.Hour + 2*time.Hour)
	return StatusPage{
		Page:      Page{Title: "Status", Lang: lang, AltURL: alt, Path: path, Version: "dev"},
		Overall:   StateDown,
		CheckedAt: now.Add(-40 * time.Second),
		Targets:   rows,
		Incidents: []IncidentRow{
			{TargetID: 2, TargetName: "Bewerbungsportal", StartedAt: now.Add(-47 * time.Minute), Duration: 47 * time.Minute},
			{TargetID: 4, TargetName: "FiveM Testserver", StartedAt: ended.Add(-12 * time.Minute), EndedAt: &ended, Duration: 12 * time.Minute},
			{TargetID: 1, TargetName: "Website", StartedAt: ended2.Add(-(2*time.Hour + 5*time.Minute)), EndedAt: &ended2, Duration: 2*time.Hour + 5*time.Minute},
		},
	}
}

func demoAdmin() AdminListPage {
	s := demoStatus("de", "/admin/")
	addr := map[int64]string{
		1: "https://dmn-software.com/",
		2: "https://bewerbungen.dmn-software.com/api/health",
		3: "10.0.0.12:5432",
		4: "fivem.example.org:30120",
		5: "https://intern.example.org/ein/ziemlich/langer/pfad/zum/healthcheck?format=json",
		6: "example.org:443",
	}
	var rows []AdminTargetRow
	for _, t := range s.Targets {
		row := AdminTargetRow{TargetRow: t, Address: addr[t.ID], Public: t.ID < 5, LastCheck: time.Now().Add(-35 * time.Second)}
		switch t.ID {
		case 2:
			row.LastError = "status 502"
		case 5:
			row.Paused, row.LastCheck = true, time.Now().Add(-20*time.Hour)
		case 6:
			row.LastCheck = time.Time{}
		}
		rows = append(rows, row)
	}
	return AdminListPage{Page: adminPage("Ziele", "/admin/"), Targets: rows, MailOK: true}
}

func adminPage(title, path string) Page {
	return Page{Title: title, Lang: "de", Path: path, Version: "dev", CSRF: "vorschau", Admin: true}
}

func demoHours(n int, step time.Duration, state func(int) State) []HourCell {
	start := time.Now().Truncate(step).Add(-time.Duration(n-1) * step)
	cells := make([]HourCell, n)
	for i := range cells {
		s := state(i)
		up := 1.0
		switch s {
		case StateDown:
			up = .25 + float64(i%3)*.2
		case StateUnknown:
			up = -1
		}
		cells[i] = HourCell{Start: start.Add(time.Duration(i) * step), State: s, Uptime: up}
	}
	return cells
}

// demoDown legt fuer jedes ziel feste ausfaelle fest, damit die vorschau bei jedem reload gleich aussieht
func demoDown(id int64) func(i, n int) bool {
	return func(i, n int) bool {
		switch id {
		case 1:
			return n > 80 && i == n/3
		case 2:
			return i == n-1 || i == n/2
		case 4:
			return i == n-27
		}
		return false
	}
}

func demoMs(id int64, i int) int {
	base := map[int64]float64{1: 140, 2: 260, 3: 36, 4: 205, 5: 90}[id]
	v := base + base*.18*math.Sin(float64(i)/5) + base*.07*math.Sin(float64(i)*1.7)
	if i%37 == 11 {
		v *= 2.4
	}
	return int(v)
}
