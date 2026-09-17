package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func sampleRows(lang string) []TargetRow {
	hours := make([]HourCell, 90)
	start := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	for i := range hours {
		hours[i] = HourCell{Start: start.Add(time.Duration(i) * time.Hour), State: StateUp, Uptime: 1}
	}
	hours[40] = HourCell{Start: hours[40].Start, State: StateDown, Uptime: .5}
	hours[41] = HourCell{Start: hours[41].Start, State: StateUnknown, Uptime: -1}

	return []TargetRow{
		{ID: 1, Name: "Website", Kind: "http", State: StateUp, Uptime24h: 1, Uptime7d: .9995, Uptime30d: .998, LatencyMs: 120, Hours: hours, Spark: HourBars(hours, time.Hour, lang)},
		{ID: 2, Name: "FiveM Testserver", Kind: "fivem", State: StateDown, Uptime24h: .9, Uptime7d: -1, Uptime30d: -1,
			Players: &Players{Count: 3, Max: 64, Version: "7290"}, Hours: hours, Spark: HourBars(hours, time.Hour, lang)},
		{ID: 3, Name: "Archiv der Bewerbungsunterlagen (Backup)", Kind: "tcp", State: StatePaused, Uptime24h: -1, Uptime7d: -1, Uptime30d: -1},
	}
}

func samplePages() map[string][]any {
	now := time.Date(2026, 9, 17, 12, 3, 0, 0, time.UTC)
	ended := now.Add(-time.Hour)
	inc := []IncidentRow{
		{TargetID: 2, TargetName: "FiveM Testserver", StartedAt: now.Add(-10 * time.Minute), Duration: 10 * time.Minute},
		{TargetID: 1, TargetName: "Website", StartedAt: ended.Add(-5 * time.Minute), EndedAt: &ended, Duration: 5 * time.Minute},
	}
	pub := func(lang string) Page {
		return Page{Title: "Status", Lang: lang, AltURL: "/en/", Path: "/", Version: "t"}
	}
	adm := Page{Title: "Ziele", Lang: "de", Path: "/admin/", Version: "t", CSRF: "tok", Admin: true}

	rows := sampleRows("de")
	var admRows []AdminTargetRow
	for _, r := range rows {
		admRows = append(admRows, AdminTargetRow{TargetRow: r, Address: "example.org:443", Paused: r.State == StatePaused, LastError: "status 502", LastCheck: now})
	}

	return map[string][]any{
		"status": {
			StatusPage{Page: pub("de"), Overall: StateDown, CheckedAt: now, Targets: rows, Incidents: inc},
			StatusPage{Page: pub("en"), Overall: StateDown, CheckedAt: now, Targets: sampleRows("en"), Incidents: inc},
			StatusPage{Page: pub("de"), Overall: StateUnknown},
		},
		"detail": {
			DetailPage{Page: pub("de"), Target: rows[0], Range: "24h", Latency: LatencyLine([]int{100, -1, 140, 90}, "de"), Incidents: inc},
			DetailPage{Page: pub("en"), Target: rows[2], Range: "30d", Latency: LatencyLine(nil, "en")},
		},
		"login": {
			LoginPage{Page: Page{Lang: "de", Path: "/admin/login"}},
			LoginPage{Page: Page{Lang: "de", Path: "/admin/login"}, Username: "leon", Error: T("de", "adm.login.locked")},
		},
		"admin_list": {
			AdminListPage{Page: adm, Targets: admRows, MailOK: true},
			AdminListPage{Page: adm},
		},
		"admin_form": {
			TargetForm{Page: adm, Kind: "http", IntervalS: 60, TimeoutMs: 10000, FailThreshold: 3},
			TargetForm{Page: adm, ID: 4, Name: "API", Kind: "tcp", Errors: map[string]string{"address": "host:port erwartet", "interval_s": "Liegt außerhalb von 30 bis 86400"}},
		},
		"notfound": {
			Page{Lang: "de"},
			Page{Lang: "en"},
		},
		"error": {
			Page{Lang: "de"},
			Page{Lang: "en"},
		},
	}
}

var (
	inlineStyle   = regexp.MustCompile(`(?i)\sstyle=`)
	inlineHandler = regexp.MustCompile(`(?i)\son[a-z]+=`)
	scriptTag     = regexp.MustCompile(`(?i)<script[^>]*>`)
)

func TestRenderPages(t *testing.T) {
	for name, list := range samplePages() {
		for i, data := range list {
			rec := httptest.NewRecorder()
			if err := Render(rec, http.StatusOK, name, data); err != nil {
				t.Fatalf("%s #%d: %v", name, i, err)
			}
			out := rec.Body.String()

			if inlineStyle.MatchString(out) {
				t.Errorf("%s #%d: style-attribut in der ausgabe", name, i)
			}
			if m := inlineHandler.FindString(out); m != "" {
				t.Errorf("%s #%d: event-handler %q in der ausgabe", name, i, m)
			}
			for _, tag := range scriptTag.FindAllString(out, -1) {
				if !strings.Contains(tag, " src=") {
					t.Errorf("%s #%d: script ohne src: %s", name, i, tag)
				}
			}
			if strings.Contains(strings.ToLower(out), "javascript:") {
				t.Errorf("%s #%d: javascript: in der ausgabe", name, i)
			}
			// ein fehlender text taucht sonst als schluessel auf
			if m := regexp.MustCompile(`>(state|overall|how|arch|inc|lat|up|nf)\.[a-z0-9.]+<`).FindString(out); m != "" {
				t.Errorf("%s #%d: text fehlt: %s", name, i, m)
			}
		}
	}
}

func TestRenderStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := Render(rec, http.StatusNotFound, "notfound", Page{Lang: "de"}); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d, wollte 404", rec.Code)
	}
	if err := Render(httptest.NewRecorder(), http.StatusOK, "gibtsnicht", nil); err == nil {
		t.Error("unbekanntes template ohne fehler")
	}
}

func TestPublicNoAdminData(t *testing.T) {
	rec := httptest.NewRecorder()
	p := samplePages()["status"][0].(StatusPage)
	if err := Render(rec, http.StatusOK, "status", p); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"example.org", "status 502", "csrf"} {
		if strings.Contains(rec.Body.String(), s) {
			t.Errorf("oeffentliche seite enthaelt %q", s)
		}
	}
}

func TestTextsComplete(t *testing.T) {
	for k := range texts["de"] {
		if strings.HasPrefix(k, "adm.") {
			continue
		}
		if _, ok := texts["en"][k]; !ok {
			t.Errorf("en: %s fehlt", k)
		}
	}
	for k := range texts["en"] {
		if _, ok := texts["de"][k]; !ok {
			t.Errorf("de: %s fehlt", k)
		}
	}
}

func TestFormat(t *testing.T) {
	cases := []struct{ got, want string }{
		{pct("de", .9995), "99,95 %"},
		{pct("en", .9995), "99.95%"},
		{pct("de", .99996), "99,99 %"},
		{pct("de", 1), "100 %"},
		{pct("de", -1), "–"},
		{dur("de", 30*time.Second), "< 1 min"},
		{dur("de", 12*time.Minute), "12 min"},
		{dur("de", 2*time.Hour+5*time.Minute), "2 h 5 min"},
		{dur("en", 50*time.Hour), "2 d 2 h"},
		{stand("de", time.Date(2026, 9, 17, 12, 3, 0, 0, time.UTC)), "Stand: 14:03 Uhr"},
		{stand("en", time.Date(2026, 1, 17, 12, 3, 0, 0, time.UTC)), "Updated 13:03"},
		{span("de", time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC), time.Date(2026, 9, 17, 10, 12, 0, 0, time.UTC)), "17.09., 12:00 bis 12:12"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%q, wollte %q", c.got, c.want)
		}
	}
}

func TestHourBars(t *testing.T) {
	c := HourBars(sampleRows("de")[0].Hours, time.Hour, "de")
	if len(c.Bars) != 90 || c.Width != 360 {
		t.Fatalf("%d segmente, breite %d", len(c.Bars), c.Width)
	}
	if c.Bars[40].Class() != "bar-down" || c.Bars[41].Class() != "bar-none" || c.Bars[0].Class() != "bar-up" {
		t.Error("klassen passen nicht zum zustand")
	}
	if c.Bars[41].H >= c.Bars[0].H {
		t.Error("segment ohne daten muss niedriger sein")
	}
	if c.Label != "Letzte 90 Stunden: 88 erreichbar, 1 mit Ausfall, 1 ohne Daten" {
		t.Errorf("label %q", c.Label)
	}
	if !strings.Contains(c.Bars[40].Title, "Ausfall") || !strings.Contains(c.Bars[41].Title, "keine Daten") {
		t.Errorf("titel %q und %q", c.Bars[40].Title, c.Bars[41].Title)
	}
	if !c.Dense() {
		t.Error("90 segmente muessen als dichte leiste gelten")
	}
	c = HourBars(sampleRows("de")[0].Hours[:24], time.Hour, "de")
	if c.Width != 96 || c.Bars[1].X != 4 {
		t.Errorf("24 segmente: breite %d", c.Width)
	}
	if c.Dense() {
		t.Error("24 segmente sind nicht dicht")
	}
}

func TestLatencyLine(t *testing.T) {
	c := LatencyLine([]int{100, -1, 200}, "de")
	if c.Line != "M0.0,90.0h0M600.0,15.0h0" || c.MaxMs != 200 {
		t.Errorf("line %q max %d", c.Line, c.MaxMs)
	}
	if c := LatencyLine([]int{100, 200, -1}, "de"); c.Line != "M0.0,90.0 300.0,15.0" {
		t.Errorf("line %q", c.Line)
	}
	if c := LatencyLine([]int{-1, -1}, "de"); c.Line != "" {
		t.Errorf("ohne messwerte trotzdem linie %q", c.Line)
	}
	if c := LatencyLine([]int{140}, "de"); c.Line != "M300.0,15.0h0" {
		t.Errorf("einzelner messwert: line %q", c.Line)
	}
}
