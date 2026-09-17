package web

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Segment 3 breit plus 1 luecke, fuer jede zellenzahl gleich. Das svg wird ohne
// seitenverhaeltnis gestreckt: 24 stunden, 30 tage und 90 stunden sind damit dasselbe
// material in verschiedener zoomstufe. Schmal holt css die luecke der dichten leiste zurueck.
const (
	barStep   = 4
	barsWide  = 30
	barHeight = 24
	barStub   = 4
)

// HourBars baut die leiste aus stunden- oder tageszellen, step ist die laenge einer zelle.
// Ohne daten gibt es nur einen stummel am boden, damit das nicht allein an der farbe haengt.
func HourBars(cells []HourCell, step time.Duration, lang string) Chart {
	c := Chart{Width: len(cells) * barStep, Height: barHeight, Bars: make([]Bar, 0, len(cells))}
	var up, down, none int
	for i, cell := range cells {
		b := Bar{X: float64(i * barStep), W: barStep - 1, H: barHeight, State: cell.State}
		switch cell.State {
		case StateUp:
			b.Y, b.H = barHeight-18, 18
			up++
		case StateDown:
			down++
		default:
			b.State = StateUnknown
			b.Y, b.H = barHeight-barStub, barStub
			none++
		}
		b.Title = barTitle(lang, cell, b.State, step)
		c.Bars = append(c.Bars, b)
	}

	key, n := "bars.h", len(cells)
	if step >= 24*time.Hour {
		key = "bars.d"
	} else if step > time.Hour {
		n = n * int(step/time.Hour)
	}
	c.Label = fmt.Sprintf(T(lang, key), n, up, down, none)
	return c
}

func barTitle(lang string, cell HourCell, s State, step time.Duration) string {
	at := when(lang, cell.Start)
	if step >= 24*time.Hour {
		at = day(lang, cell.Start)
	}
	switch s {
	case StateUnknown:
		return at + " · " + T(lang, "bars.none")
	case StateDown:
		// die prozentzahl allein liest sich wie ein normaler wert, das wort aus der legende davor
		return at + " · " + T(lang, "leg.down") + " · " + pct(lang, cell.Uptime)
	}
	return at + " · " + pct(lang, cell.Uptime)
}

// Dense meldet die dichte leiste (90 stunden). Schmal ist dort jede zelle nur wenige pixel
// breit, css nimmt die luecken dann zurueck.
func (c Chart) Dense() bool { return len(c.Bars) > barsWide }

func (b Bar) Class() string {
	switch b.State {
	case StateUp:
		return "bar-up"
	case StateDown:
		return "bar-down"
	}
	return "bar-none"
}

// Oben bleiben latTop einheiten frei, dort zieht das template die linie fuer den hoechsten wert.
const (
	latWidth  = 600
	latHeight = 165
	latTop    = 15
)

// MaxY ist die hoehe der gestrichelten oberkante; das template soll latTop nicht abschreiben.
func (c Chart) MaxY() int { return latTop }

// LatencyLine rechnet den pfad fuer das diagramm, gleich verteilt ueber den zeitraum.
// Werte unter 0 sind schritte ohne messung, dort bricht die linie ab. Ein einzelner
// messwert zwischen zwei luecken wird mit h0 zum punkt, sonst waere er unsichtbar.
func LatencyLine(ms []int, lang string) Chart {
	c := Chart{Width: latWidth, Height: latHeight}
	seen := false
	for _, v := range ms {
		if v >= 0 {
			seen = true
			c.MaxMs = max(c.MaxMs, v)
		}
	}
	if !seen {
		c.Label = T(lang, "lat.none")
		return c
	}

	top := float64(max(c.MaxMs, 1))
	span := float64(latHeight - latTop)
	var b strings.Builder
	run := 0
	for i, v := range ms {
		if v < 0 {
			if run == 1 {
				b.WriteString("h0")
			}
			run = 0
			continue
		}
		// bei nur einem schritt steht der punkt in der mitte des zeitraums, nicht am rand
		x := float64(latWidth) / 2
		if len(ms) > 1 {
			x = float64(i) * latWidth / float64(len(ms)-1)
		}
		y := latHeight - float64(v)/top*span
		if run == 0 {
			b.WriteByte('M')
		} else {
			b.WriteByte(' ')
		}
		run++
		b.WriteString(strconv.FormatFloat(x, 'f', 1, 64))
		b.WriteByte(',')
		b.WriteString(strconv.FormatFloat(y, 'f', 1, 64))
	}
	if run == 1 {
		b.WriteString("h0")
	}
	c.Line = b.String()
	c.Label = fmt.Sprintf(T(lang, "lat.label"), c.MaxMs)
	return c
}
