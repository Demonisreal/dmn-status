package web

import "time"

// Die Structs hier sind alles, was die Templates zu sehen bekommen. Handler bauen sie aus
// dem Store, die Vorschau im Dev-Modus aus festen Beispieldaten.

type State string

const (
	StateUp      State = "up"
	StateDown    State = "down"
	StateUnknown State = "unknown"
	StatePaused  State = "paused"
)

type Page struct {
	Title     string
	Lang      string // de oder en, admin immer de
	AltURL    string // gleiche seite in der anderen sprache, leer im admin
	Path      string // fuer aria-current in der navigation
	Version   string // haengt als ?v= an css und js
	CSRF      string // leer auf oeffentlichen seiten
	Admin     bool
	Flash     string
	FlashWarn bool // die aktion ist nicht durchgelaufen, z. b. testmail fehlgeschlagen
}

type StatusPage struct {
	Page
	Overall   State
	CheckedAt time.Time
	Targets   []TargetRow
	Incidents []IncidentRow
}

type TargetRow struct {
	ID        int64
	Name      string
	Kind      string // http, tcp, fivem
	State     State
	Uptime24h float64 // 0..1, -1 wenn keine daten
	Uptime7d  float64
	Uptime30d float64
	LatencyMs int
	Players   *Players
	Hours     []HourCell // letzte 90 stunden, aelteste zuerst
	Spark     Chart
}

type Players struct {
	Count   int
	Max     int
	Version string
}

type HourCell struct {
	Start time.Time
	// State ist unknown, wenn in der stunde nicht geprueft wurde
	State  State
	Uptime float64
}

type IncidentRow struct {
	TargetID   int64
	TargetName string
	StartedAt  time.Time
	EndedAt    *time.Time
	Duration   time.Duration
	Cause      string // nur im admin befuellt
}

type DetailPage struct {
	Page
	Target    TargetRow
	Range     string // 24h, 7d, 30d
	Latency   Chart
	Incidents []IncidentRow
}

// Chart ist schon fertig gerechnet, das Template setzt nur noch Attribute.
type Chart struct {
	Width, Height int
	Line          string // d fuer <path>, bricht bei luecken ab
	Bars          []Bar
	MaxMs         int
	Label         string // aria-label
}

type Bar struct {
	X, Y, W, H float64
	State      State
	Title      string
}

type LoginPage struct {
	Page
	Username string
	Error    string
}

type AdminListPage struct {
	Page
	Targets []AdminTargetRow
	MailOK  bool // smtp konfiguriert
}

type AdminTargetRow struct {
	TargetRow
	Address   string
	Paused    bool
	Public    bool
	LastError string
	LastCheck time.Time
}

type TargetForm struct {
	Page
	ID            int64 // 0 beim anlegen
	Name          string
	Kind          string
	Address       string
	ExpectStatus  string
	Keyword       string
	ConnectTo     string
	IntervalS     int
	TimeoutMs     int
	FailThreshold int
	Public        bool
	Paused        bool
	Errors        map[string]string // feldname -> meldung
}
