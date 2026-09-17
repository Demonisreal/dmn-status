package web

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

const (
	loginWindow = 15 * time.Minute
	loginPerIP  = 5
	loginGlobal = 30
	loginIPs    = 10000
)

type window struct {
	start  time.Time
	n      int
	warned bool
}

// limiter zaehlt login-versuche je ip und insgesamt, jeweils in festen 15-minuten-fenstern.
// Gezaehlt wird vor dem passwortvergleich, ein erfolgreicher login nimmt den versuch zurueck.
type limiter struct {
	mu     sync.Mutex
	ips    map[string]*window
	global window
	swept  time.Time
}

func newLimiter() *limiter {
	return &limiter{ips: map[string]*window{}}
}

// take bucht einen versuch. Ist das limit erreicht, kommt die wartezeit zurueck und es wird
// nichts gebucht. Ein bekanntes geraet zaehlt global mit, wird aber vom globalen limit nicht
// gesperrt: so kommt der admin auch waehrend eines verteilten angriffs noch rein.
// first ist nur bei der ersten abweisung im fenster gesetzt, damit eine flut nicht das log fuellt.
func (l *limiter) take(ip string, now time.Time, knownDevice bool) (wait time.Duration, first bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.swept) >= loginWindow || len(l.ips) >= loginIPs {
		for k, w := range l.ips {
			if now.Sub(w.start) >= loginWindow {
				delete(l.ips, k)
			}
		}
		l.swept = now
	}
	if now.Sub(l.global.start) >= loginWindow {
		l.global = window{start: now}
	}

	w := l.ips[ip]
	if w != nil && now.Sub(w.start) >= loginWindow {
		w = nil
	}
	switch {
	case w != nil && w.n >= loginPerIP:
		return w.start.Add(loginWindow).Sub(now), once(&w.warned)
	case !knownDevice && l.global.n >= loginGlobal:
		return l.global.start.Add(loginWindow).Sub(now), once(&l.global.warned)
	case w == nil && len(l.ips) >= loginIPs:
		return loginWindow, once(&l.global.warned)
	}
	if w == nil {
		w = &window{start: now}
		l.ips[ip] = w
	}
	w.n++
	l.global.n++
	return 0, false
}

func once(done *bool) bool {
	first := !*done
	*done = true
	return first
}

func (l *limiter) forgive(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.ips, ip)
	if l.global.n > 0 {
		l.global.n--
	}
}

// clientIP nimmt RemoteAddr. X-Forwarded-For zaehlt nur, wenn RemoteAddr in TRUSTED_PROXY liegt,
// und dann nur der letzte eintrag: den hat der proxy selbst angehaengt, alles davor kommt vom client.
// IPv6 wird auf /64 gekuerzt, ein einzelner anschluss hat meist das ganze netz.
func (s *server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	ip = ip.Unmap().WithZone("")

	xff := r.Header.Values("X-Forwarded-For")
	if proxy := s.Config.TrustedProxy; proxy.IsValid() && proxy.Contains(ip) && len(xff) > 0 {
		last := xff[len(xff)-1]
		if i := strings.LastIndexByte(last, ','); i >= 0 {
			last = last[i+1:]
		}
		if fwd, err := netip.ParseAddr(strings.TrimSpace(last)); err == nil {
			ip = fwd.Unmap().WithZone("")
		}
	}
	if ip.Is6() {
		p, _ := ip.Prefix(64)
		return p.String()
	}
	return ip.String()
}
