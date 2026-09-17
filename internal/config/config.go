// Package config liest die Einstellungen aus der Umgebung.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Config struct {
	Addr          string
	DBPath        string
	BaseURL       string       // ohne abschliessenden slash, leer wenn nicht gesetzt
	TrustedProxy  netip.Prefix // ohne gueltiges netz zaehlt X-Forwarded-For nie
	CookieSecure  bool
	PrivateAllow  []string // host:port, nur fuer connect_to
	RetentionDays int
	MetricsToken  string // leer schaltet /metrics ab

	SMTPHost string
	SMTPPort int
	SMTPUser string
	SMTPPass string
	MailFrom string
	MailTo   []string // nur die adressen, ohne anzeigenamen

	// nur fuer den ersten start, solange noch kein admin in der datenbank steht
	AdminUser     string
	AdminPassword string
}

// MailEnabled ist true, wenn SMTP_HOST und MAIL_TO gesetzt sind.
func (c Config) MailEnabled() bool {
	return c.SMTPHost != "" && len(c.MailTo) > 0
}

func Load() (Config, error) {
	return load(os.Getenv)
}

type env struct {
	get  func(string) string
	errs []error
}

func (e *env) fail(key, msg string) {
	e.errs = append(e.errs, fmt.Errorf("%s: %s", key, msg))
}

func (e *env) str(key, def string) string {
	if v := strings.TrimSpace(e.get(key)); v != "" {
		return v
	}
	return def
}

func (e *env) flag(key string, def bool) bool {
	v := e.str(key, "")
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.fail(key, "true oder false erwartet")
	}
	return b
}

func (e *env) num(key string, def, lo, hi int) int {
	v := e.str(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		e.fail(key, fmt.Sprintf("zahl von %d bis %d erwartet", lo, hi))
	}
	return n
}

func load(get func(string) string) (Config, error) {
	e := &env{get: get}
	c := Config{
		Addr:          e.str("ADDR", ":8080"),
		DBPath:        e.str("DB_PATH", "data/status.db"),
		BaseURL:       strings.TrimRight(e.str("BASE_URL", ""), "/"),
		CookieSecure:  e.flag("COOKIE_SECURE", true),
		RetentionDays: e.num("RETENTION_DAYS", 30, 1, 400),
		MetricsToken:  e.str("METRICS_TOKEN", ""),
		SMTPHost:      e.str("SMTP_HOST", ""),
		SMTPPort:      e.num("SMTP_PORT", 465, 1, 65535),
		SMTPUser:      e.str("SMTP_USER", ""),
		// passwoerter nicht trimmen, leerzeichen am rand koennen dazugehoeren
		SMTPPass:      get("SMTP_PASS"),
		MailFrom:      e.str("MAIL_FROM", ""),
		AdminUser:     e.str("ADMIN_USER", ""),
		AdminPassword: get("ADMIN_PASSWORD"),
	}

	if _, port, err := net.SplitHostPort(c.Addr); err != nil || port == "" {
		e.fail("ADDR", "host:port erwartet, z. b. :8080")
	}
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			e.fail("BASE_URL", "z. b. https://status.example.com")
		}
	}
	// TRUST_PROXY=true still zu ignorieren hiesse, alle besucher teilen sich die ip des proxys
	// und damit die login-sperre
	if e.str("TRUST_PROXY", "") != "" {
		e.fail("TRUST_PROXY", "entfernt, stattdessen TRUSTED_PROXY=cidr des proxy-netzes")
	}
	if v := e.str("TRUSTED_PROXY", ""); v != "" {
		p, err := netip.ParsePrefix(v)
		if err != nil {
			e.fail("TRUSTED_PROXY", "cidr erwartet, z. b. 172.20.0.0/24")
		}
		c.TrustedProxy = p.Masked()
	}
	// alte .env-dateien sollen laut scheitern statt still alle privaten ziele zu sperren
	if e.str("ALLOW_PRIVATE", "") != "" {
		e.fail("ALLOW_PRIVATE", "entfernt, stattdessen PRIVATE_ALLOW=host:port,...")
	}
	for s := range strings.SplitSeq(e.str("PRIVATE_ALLOW", ""), ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		host, port, err := net.SplitHostPort(s)
		n, perr := strconv.ParseUint(port, 10, 16)
		if err != nil || host == "" || strings.ContainsFunc(host, unicode.IsSpace) || perr != nil || n == 0 {
			e.fail("PRIVATE_ALLOW", fmt.Sprintf("%q: host:port mit port 1 bis 65535 erwartet", s))
			continue
		}
		c.PrivateAllow = append(c.PrivateAllow, s)
	}
	if c.MetricsToken != "" && len(c.MetricsToken) < 32 {
		e.fail("METRICS_TOKEN", "mindestens 32 zeichen")
	}

	if to := e.str("MAIL_TO", ""); to != "" {
		list, err := mail.ParseAddressList(to)
		if err != nil {
			e.fail("MAIL_TO", "adressliste ungueltig")
		}
		for _, a := range list {
			c.MailTo = append(c.MailTo, a.Address)
		}
	}
	if c.SMTPHost == "" && len(c.MailTo) > 0 {
		e.fail("SMTP_HOST", "fehlt, MAIL_TO ist gesetzt")
	}
	if c.MailEnabled() {
		if c.MailFrom == "" {
			e.fail("MAIL_FROM", "fehlt")
		} else if _, err := mail.ParseAddress(c.MailFrom); err != nil {
			e.fail("MAIL_FROM", "adresse ungueltig")
		}
		if (c.SMTPUser == "") != (c.SMTPPass == "") {
			e.fail("SMTP_USER", "SMTP_USER und SMTP_PASS nur zusammen")
		}
	}

	if (c.AdminUser == "") != (c.AdminPassword == "") {
		e.fail("ADMIN_USER", "ADMIN_USER und ADMIN_PASSWORD nur zusammen")
	}
	if c.AdminUser != "" {
		if utf8.RuneCountInString(c.AdminUser) > 60 || strings.ContainsFunc(c.AdminUser, unicode.IsSpace) ||
			strings.ContainsFunc(c.AdminUser, unicode.IsControl) {
			e.fail("ADMIN_USER", "bis 60 zeichen, keine leerzeichen")
		}
		if utf8.RuneCountInString(c.AdminPassword) < 12 {
			e.fail("ADMIN_PASSWORD", "mindestens 12 zeichen")
		}
	}

	return c, errors.Join(e.errs...)
}
