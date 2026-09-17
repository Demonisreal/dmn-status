package web

import (
	"fmt"
	"html/template"
	"math"
	"strconv"
	"strings"
	"time"

	// im docker-image gibt es keine zoneinfo, ohne das faellt LoadLocation auf UTC
	_ "time/tzdata"
)

var berlin, _ = time.LoadLocation("Europe/Berlin")

// admin gibt es nur auf deutsch, die adm.-schluessel fehlen deshalb im englischen teil
var texts = map[string]map[string]string{
	"de": {
		"meta.desc":     "Verfügbarkeit und Vorfälle der Dienste von DMN Software, gemessen von einem eigenen Go-Dienst.",
		"skip":          "Zum Inhalt",
		"main.id":       "inhalt",
		"nav.label":     "Abschnitte",
		"lang.label":    "Sprache",
		"nav.svc":       "Dienste",
		"nav.inc":       "Vorfälle",
		"nav.how":       "So funktioniert's",
		"id.svc":        "dienste",
		"id.inc":        "vorfaelle",
		"id.how":        "so-funktionierts",
		"crumb.proj":    "Projekte",
		"url.proj":      "https://dmn-software.com/#projekte",
		"foot.label":    "Rechtliches",
		"foot.imp":      "Impressum",
		"url.imp":       "https://dmn-software.com/impressum.html",
		"foot.priv":     "Datenschutz",
		"url.priv":      "https://dmn-software.com/datenschutz.html",
		"state.up":      "erreichbar",
		"state.down":    "nicht erreichbar",
		"state.unknown": "keine Daten",
		"state.paused":  "pausiert",

		"overall.up":      "Alle Dienste erreichbar",
		"overall.down1":   "1 Dienst nicht erreichbar",
		"overall.downN":   "%d Dienste nicht erreichbar",
		"overall.unknown": "Noch keine Daten",
		"overall.paused":  "Prüfungen pausiert",
		"overall.empty":   "Noch keine Dienste eingetragen",
		"stand":           "Stand: %s Uhr",
		"stand.none":      "Stand: unbekannt",

		"svc.h":    "Dienste",
		"svc.note": "Je Segment eine Stunde, die letzten 90 Stunden",
		"leg.up":   "erreichbar",
		"leg.down": "Ausfall",
		"leg.none": "keine Daten",
		"up.24h":   "24 h",
		"up.7d":    "7 T",
		"up.30d":   "30 T",
		"up.label": "Uptime",
		"players":  "Spieler %d/%d",
		"version":  "Version %s",

		"inc.h":      "Vorfälle",
		"inc.none":   "Keine Vorfälle in den letzten 30 Tagen",
		"inc.none.r": "Keine Vorfälle in diesem Zeitraum",
		"inc.live":   "läuft",
		"inc.done":   "beendet",
		"inc.since":  "seit %s",
		"inc.span":   "%s bis %s",

		"how.h":        "So funktioniert's",
		"how.lead":     "Ein Go-Binary ohne Framework, die Messwerte liegen in SQLite.",
		"how.cap":      "Weg einer Prüfung vom Ziel bis zur Statusseite und zur Mail.",
		"how.checks.t": "Checks",
		"how.checks":   "HTTP prüft den Statuscode und auf Wunsch ein Stichwort im Antworttext. TCP prüft nur, ob der Port eine Verbindung annimmt. FiveM fragt info.json und players.json des FXServers ab, angezeigt werden nur Spielerzahl und Version.",
		"how.sched.t":  "Scheduler",
		"how.sched":    "Jedes Ziel läuft in einer eigenen Goroutine mit eigenem Intervall. Ein kleiner zufälliger Versatz (Jitter) verhindert, dass alle Prüfungen im selben Moment starten.",
		"how.inc.t":    "Vorfälle",
		"how.inc":      "Ein Vorfall beginnt erst nach mehreren Fehlschlägen in Folge, ein einzelner Aussetzer löst keinen Alarm aus. Bei Ausfall und bei Wiederkehr geht eine Mail raus.",
		"how.sec.t":    "Sicherheit",
		"how.sec":      "Passwörter mit argon2id, CSRF-Token in jedem Formular, Prüfungen auf interne Adressen sind gesperrt (SSRF), dazu eine strenge CSP ohne Inline-Skripte. Öffentlich erscheinen weder Adressen noch Fehlertexte.",
		"arch.targets": "Ziele",
		"arch.sched":   "Goroutine je Ziel",
		"arch.checks":  "mit Timeout",
		"arch.db":      "Checks, Stunden",
		"arch.page":    "Statusseite",
		"arch.inc":     "Vorfall",
		"arch.inc.sub": "ab n Fehlschlägen",
		"arch.mail":    "Ausfall, Wiederkehr",

		"range.label": "Zeitraum",
		"range.24h":   "24 Stunden",
		"range.7d":    "7 Tage",
		"range.30d":   "30 Tage",
		"lat.h":       "Antwortzeit",
		"lat.max":     "max %d ms",
		"lat.latest":  "Letzte Antwortzeit",
		"lat.none":    "Noch keine Messwerte",
		"lat.label":   "Antwortzeit im Zeitraum, höchster Wert %d ms",
		"avail.h":     "Verfügbarkeit",
		"ago.24h":     "vor 24 h",
		"ago.7d":      "vor 7 T",
		"ago.30d":     "vor 30 T",
		"now":         "jetzt",
		"paused.note": "Die Prüfung ist pausiert, es kommen keine neuen Messwerte dazu.",
		"bars.h":      "Letzte %d Stunden: %d erreichbar, %d mit Ausfall, %d ohne Daten",
		"bars.d":      "Letzte %d Tage: %d erreichbar, %d mit Ausfall, %d ohne Daten",
		"bars.none":   "keine Daten",

		"nf.title": "Seite nicht gefunden",
		"nf.lead":  "Diese Adresse gibt es hier nicht. Vielleicht ist der Link veraltet oder hat sich ein Tippfehler eingeschlichen.",
		"nf.back":  "Zur Statusseite",

		"err.title": "Interner Fehler",
		"err.lead":  "Beim Aufbau dieser Seite ist etwas schiefgegangen. Bitte in ein paar Minuten erneut versuchen.",

		"adm.login.bad":    "Benutzername oder Passwort falsch.",
		"adm.login.locked": "Zu viele Fehlversuche. Bitte später erneut versuchen.",
		"adm.csrf":         "Das Formular ist abgelaufen. Bitte erneut absenden.",
		"adm.saved":        "Ziel gespeichert.",
		"adm.deleted":      "Ziel gelöscht.",
		"adm.mail.sent":    "Testmail verschickt.",
		"adm.mail.failed":  "Testmail konnte nicht verschickt werden.",
		"adm.mail.off":     "Mailversand ist nicht eingerichtet.",
		"adm.checked":      "Prüfung angestoßen.",
		"adm.check.paused": "Das Ziel ist pausiert und wurde nicht geprüft.",
		"adm.check.rate":   "Die Prüfung wurde gerade erst angestoßen. Bitte kurz warten.",
	},
	"en": {
		"meta.desc":     "Availability and incidents of the DMN Software services, measured by a small Go service.",
		"skip":          "Skip to content",
		"main.id":       "content",
		"nav.label":     "Sections",
		"lang.label":    "Language",
		"nav.svc":       "Services",
		"nav.inc":       "Incidents",
		"nav.how":       "How it works",
		"id.svc":        "services",
		"id.inc":        "incidents",
		"id.how":        "how-it-works",
		"crumb.proj":    "Projects",
		"url.proj":      "https://dmn-software.com/en/#projects",
		"foot.label":    "Legal",
		"foot.imp":      "Legal notice",
		"url.imp":       "https://dmn-software.com/en/impressum.html",
		"foot.priv":     "Privacy policy",
		"url.priv":      "https://dmn-software.com/en/datenschutz.html",
		"state.up":      "reachable",
		"state.down":    "not reachable",
		"state.unknown": "no data",
		"state.paused":  "paused",

		"overall.up":      "All services reachable",
		"overall.down1":   "1 service not reachable",
		"overall.downN":   "%d services not reachable",
		"overall.unknown": "No data yet",
		"overall.paused":  "Checks paused",
		"overall.empty":   "No services added yet",
		"stand":           "Updated %s",
		"stand.none":      "Status unknown",

		"svc.h":    "Services",
		"svc.note": "One segment per hour, last 90 hours",
		"leg.up":   "reachable",
		"leg.down": "outage",
		"leg.none": "no data",
		"up.24h":   "24h",
		"up.7d":    "7d",
		"up.30d":   "30d",
		"up.label": "Uptime",
		"players":  "Players %d/%d",
		"version":  "Version %s",

		"inc.h":      "Incidents",
		"inc.none":   "No incidents in the last 30 days",
		"inc.none.r": "No incidents in this range",
		"inc.live":   "ongoing",
		"inc.done":   "resolved",
		"inc.since":  "since %s",
		"inc.span":   "%s to %s",

		"how.h":        "How it works",
		"how.lead":     "A single Go binary without a framework, measurements are stored in SQLite.",
		"how.cap":      "Path of a check from the target to the status page and the mail.",
		"how.checks.t": "Checks",
		"how.checks":   "HTTP checks the status code and optionally a keyword in the response body. TCP only checks whether the port accepts a connection. FiveM queries info.json and players.json of the FXServer, only the player count and version are shown.",
		"how.sched.t":  "Scheduler",
		"how.sched":    "Every target runs in its own goroutine with its own interval. A small random offset (jitter) keeps all checks from starting at the same moment.",
		"how.inc.t":    "Incidents",
		"how.inc":      "An incident only starts after several failed checks in a row, a single hiccup does not raise an alert. A mail goes out when a service goes down and when it is back.",
		"how.sec.t":    "Security",
		"how.sec":      "Passwords hashed with argon2id, a CSRF token in every form, checks against internal addresses are blocked (SSRF), plus a strict CSP without inline scripts. Addresses and error messages are never shown publicly.",
		"arch.targets": "Targets",
		"arch.sched":   "goroutine per target",
		"arch.checks":  "with timeout",
		"arch.db":      "checks, hours",
		"arch.page":    "Status page",
		"arch.inc":     "Incident",
		"arch.inc.sub": "after n failures",
		"arch.mail":    "down and recovery",

		"range.label": "Range",
		"range.24h":   "24 hours",
		"range.7d":    "7 days",
		"range.30d":   "30 days",
		"lat.h":       "Response time",
		"lat.max":     "max %d ms",
		"lat.latest":  "Latest response time",
		"lat.none":    "No measurements yet",
		"lat.label":   "Response time in this range, peak %d ms",
		"avail.h":     "Availability",
		"ago.24h":     "24h ago",
		"ago.7d":      "7d ago",
		"ago.30d":     "30d ago",
		"now":         "now",
		"paused.note": "Checks are paused, no new measurements are recorded.",
		"bars.h":      "Last %d hours: %d reachable, %d with outage, %d without data",
		"bars.d":      "Last %d days: %d reachable, %d with outage, %d without data",
		"bars.none":   "no data",

		"nf.title": "Page not found",
		"nf.lead":  "This address does not exist here. The link may be outdated or contain a typo.",
		"nf.back":  "To the status page",

		"err.title": "Internal error",
		"err.lead":  "Something went wrong while building this page. Please try again in a few minutes.",
	},
}

// T liefert den text fuer lang, faellt auf deutsch zurueck und zeigt sonst den schluessel,
// damit ein vergessener eintrag in der vorschau auffaellt statt leer zu bleiben.
func T(lang, key string) string {
	if s, ok := texts[lang][key]; ok {
		return s
	}
	if s, ok := texts["de"][key]; ok {
		return s
	}
	return key
}

func Funcs() template.FuncMap {
	return template.FuncMap{
		"t":       T,
		"tf":      func(lang, key string, args ...any) string { return fmt.Sprintf(T(lang, key), args...) },
		"pct":     pct,
		"dur":     dur,
		"stand":   stand,
		"clock":   clock,
		"day":     day,
		"when":    when,
		"base":    base,
		"kind":    kindName,
		"overall": overall,
		"span":    span,
	}
}

// pct rundet ab, 99,996 % darf nicht als 100 % erscheinen. Immer zwei nachkommastellen,
// sonst stehen 99,7 und 99,95 in der rechtsbuendigen spalte versetzt.
func pct(lang string, v float64) string {
	if v < 0 {
		return "–"
	}
	s := strconv.FormatFloat(math.Floor(v*10000+1e-6)/100, 'f', 2, 64)
	if s == "100.00" {
		s = "100"
	}
	if lang == "en" {
		return s + "%"
	}
	return strings.Replace(s, ".", ",", 1) + " %"
}

func dur(lang string, d time.Duration) string {
	m := int(d.Round(time.Minute) / time.Minute)
	switch {
	case d < time.Minute:
		return "< 1 min"
	case m < 60:
		return fmt.Sprintf("%d min", m)
	case m < 24*60:
		if m%60 == 0 {
			return fmt.Sprintf("%d h", m/60)
		}
		return fmt.Sprintf("%d h %d min", m/60, m%60)
	}
	unit := "T"
	if lang == "en" {
		unit = "d"
	}
	if h := m / 60 % 24; h > 0 {
		return fmt.Sprintf("%d %s %d h", m/(24*60), unit, h)
	}
	return fmt.Sprintf("%d %s", m/(24*60), unit)
}

func clock(t time.Time) string {
	return t.In(berlin).Format("15:04")
}

func stand(lang string, t time.Time) string {
	if t.IsZero() {
		return T(lang, "stand.none")
	}
	return fmt.Sprintf(T(lang, "stand"), clock(t))
}

func day(lang string, t time.Time) string {
	if lang == "en" {
		return t.In(berlin).Format("Jan 2, 2006")
	}
	return t.In(berlin).Format("02.01.2006")
}

func when(lang string, t time.Time) string {
	if lang == "en" {
		return t.In(berlin).Format("Jan 2, 15:04")
	}
	return t.In(berlin).Format("02.01., 15:04")
}

// span laesst das datum am ende weg, wenn der vorfall am selben tag endete
func span(lang string, from, to time.Time) string {
	end := when(lang, to)
	if from.In(berlin).Format(time.DateOnly) == to.In(berlin).Format(time.DateOnly) {
		end = clock(to)
	}
	return fmt.Sprintf(T(lang, "inc.span"), when(lang, from), end)
}

// base ist das pfadpraefix der sprache, deutsch liegt unter /
func base(lang string) string {
	if lang == "en" {
		return "/en"
	}
	return ""
}

func kindName(k string) string {
	switch k {
	case "http":
		return "HTTP"
	case "tcp":
		return "TCP"
	case "fivem":
		return "FiveM"
	}
	return k
}

func overall(lang string, s State, rows []TargetRow) string {
	if len(rows) == 0 {
		return T(lang, "overall.empty")
	}
	switch s {
	case StateUp:
		return T(lang, "overall.up")
	case StatePaused:
		return T(lang, "overall.paused")
	case StateDown:
		n := 0
		for _, r := range rows {
			if r.State == StateDown {
				n++
			}
		}
		if n <= 1 {
			return T(lang, "overall.down1")
		}
		return fmt.Sprintf(T(lang, "overall.downN"), n)
	}
	return T(lang, "overall.unknown")
}
