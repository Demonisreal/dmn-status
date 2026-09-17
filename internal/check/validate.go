package check

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var hostname = regexp.MustCompile(`^([A-Za-z0-9_]([A-Za-z0-9_-]{0,61}[A-Za-z0-9])?\.)*[A-Za-z0-9_]([A-Za-z0-9_-]{0,61}[A-Za-z0-9])?\.?$`)

// Validate prueft die Eingaben aus dem Admin-Formular. Schluessel sind die Spaltennamen
// (name, kind, address, expect_status, keyword, connect_to, interval_s, timeout_ms,
// fail_threshold), leere Map heisst gueltig.
func Validate(t Target) map[string]string {
	errs := map[string]string{}

	if name := strings.TrimSpace(t.Name); name == "" || utf8.RuneCountInString(name) > 60 {
		errs["name"] = "1 bis 60 Zeichen"
	}

	switch t.Kind {
	case KindHTTP:
		if msg := checkURL(t.Address); msg != "" {
			errs["address"] = msg
		}
		if _, err := parseStatus(t.ExpectStatus); err != nil || len(t.ExpectStatus) > 100 {
			errs["expect_status"] = "Unbekanntes Format, erlaubt sind Codes, Bereiche und Listen"
		}
	case KindTCP, KindFiveM:
		if !hostPort(t.Address) {
			errs["address"] = "Adresse als host:port erwartet"
		}
	default:
		errs["kind"] = "HTTP, TCP oder FiveM"
	}

	switch {
	case utf8.RuneCountInString(t.Keyword) > 200:
		errs["keyword"] = "Höchstens 200 Zeichen"
	case t.Keyword != "" && t.Kind != KindHTTP:
		errs["keyword"] = "Nur bei HTTP"
	}

	switch {
	case t.ConnectTo == "":
	case t.Kind == KindTCP:
		errs["connect_to"] = "Nur bei HTTP und FiveM"
	case !hostPort(t.ConnectTo):
		errs["connect_to"] = "Leer oder host:port"
	}

	if t.IntervalS < 30 || t.IntervalS > 86400 {
		errs["interval_s"] = "Liegt außerhalb von 30 bis 86400"
	} else if hi := min(60000, t.IntervalS*1000-1); t.TimeoutMs < 500 || t.TimeoutMs > hi {
		errs["timeout_ms"] = fmt.Sprintf("500 bis %d ms", hi)
	}
	if t.FailThreshold < 1 || t.FailThreshold > 20 {
		errs["fail_threshold"] = "Liegt außerhalb von 1 bis 20"
	}

	// zuletzt, damit diese meldung eine laengen- oder formatmeldung ueberschreibt
	for key, v := range map[string]string{"name": t.Name, "address": t.Address, "keyword": t.Keyword, "connect_to": t.ConnectTo} {
		if strings.ContainsFunc(v, hidden) {
			errs[key] = "Keine Steuer- oder Formatzeichen"
		}
	}
	return errs
}

func checkURL(s string) string {
	if len(s) > 2000 {
		return "Adresse zu lang"
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" {
		return "Adresse mit http:// oder https:// erwartet"
	}
	if u.User != nil {
		return "Keine Zugangsdaten in der Adresse"
	}
	if !validHost(u.Hostname()) {
		return "Host ungültig"
	}
	if p := u.Port(); p != "" && !validPort(p) {
		return "Port 1 bis 65535"
	}
	return ""
}

func hostPort(s string) bool {
	host, port, err := net.SplitHostPort(s)
	return err == nil && validHost(host) && validPort(port)
}

func validHost(h string) bool {
	if ip, err := netip.ParseAddr(h); err == nil {
		return ip.Zone() == ""
	}
	return len(h) <= 253 && hostname.MatchString(h)
}

// hidden trifft auch Formatzeichen wie U+202E oder U+200B, die im Admin Text umdrehen oder
// unsichtbar machen und von unicode.IsControl nicht erfasst werden.
func hidden(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}

func validPort(p string) bool {
	n, err := strconv.ParseUint(p, 10, 16)
	return err == nil && n > 0
}
