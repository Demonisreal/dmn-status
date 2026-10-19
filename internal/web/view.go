package web

import "time"

type State string

const (
	StateUp      State = "up"
	StateDown    State = "down"
	StateUnknown State = "unknown"
	StatePaused  State = "paused"
)

type Page struct {
	Title     string
	Lang      string
	AltURL    string
	Path      string
	Version   string
	CSRF      string
	Admin     bool
	Flash     string
	FlashWarn bool
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
	Kind      string
	State     State
	Uptime24h float64
	Uptime7d  float64
	Uptime30d float64
	LatencyMs int
	Players   *Players
	Hours     []HourCell
	Spark     Chart
}

type Players struct {
	Count   int
	Max     int
	Version string
}

type HourCell struct {
	Start  time.Time
	State  State
	Uptime float64
}

type IncidentRow struct {
	TargetID   int64
	TargetName string
	StartedAt  time.Time
	EndedAt    *time.Time
	Duration   time.Duration
	Cause      string
}

type DetailPage struct {
	Page
	Target    TargetRow
	Range     string
	Latency   Chart
	Incidents []IncidentRow
}

type Chart struct {
	Width, Height int
	Line          string
	Bars          []Bar
	MaxMs         int
	Label         string
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
	MailOK  bool
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
	ID            int64
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
	Errors        map[string]string
}
