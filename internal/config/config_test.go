package config

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func fromMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, err := load(fromMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":8080" || c.DBPath != "data/status.db" || !c.CookieSecure || c.TrustedProxy.IsValid() ||
		c.PrivateAllow != nil || c.RetentionDays != 30 || c.SMTPPort != 465 || c.MailEnabled() {
		t.Fatalf("defaults %+v", c)
	}
}

func TestFull(t *testing.T) {
	c, err := load(fromMap(map[string]string{
		"ADDR":           "127.0.0.1:9000",
		"DB_PATH":        "/data/status.db",
		"BASE_URL":       "https://status.example.com/",
		"TRUSTED_PROXY":  "172.20.0.9/24",
		"COOKIE_SECURE":  "false",
		"PRIVATE_ALLOW":  " proxy:443, proxy:80,,[fd00::5]:8443 ",
		"RETENTION_DAYS": "14",
		"METRICS_TOKEN":  strings.Repeat("a", 32),
		"SMTP_HOST":      "smtp.example.com",
		"SMTP_PORT":      "587",
		"SMTP_USER":      "status",
		"SMTP_PASS":      " geheim ",
		"MAIL_FROM":      "DMN Status <status@example.com>",
		"MAIL_TO":        "a@example.com, Leon <b@example.com>",
		"ADMIN_USER":     "leon",
		"ADMIN_PASSWORD": "lang genug fuer den test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "https://status.example.com" {
		t.Errorf("BaseURL %q", c.BaseURL)
	}
	if c.TrustedProxy != netip.MustParsePrefix("172.20.0.0/24") || c.CookieSecure || c.RetentionDays != 14 || c.SMTPPort != 587 {
		t.Errorf("werte %+v", c)
	}
	if !slices.Equal(c.PrivateAllow, []string{"proxy:443", "proxy:80", "[fd00::5]:8443"}) {
		t.Errorf("PrivateAllow %q", c.PrivateAllow)
	}
	if c.SMTPPass != " geheim " {
		t.Errorf("SMTP_PASS getrimmt: %q", c.SMTPPass)
	}
	if !c.MailEnabled() || !slices.Equal(c.MailTo, []string{"a@example.com", "b@example.com"}) {
		t.Errorf("MailTo %v", c.MailTo)
	}
}

func TestInvalid(t *testing.T) {
	mail := map[string]string{
		"SMTP_HOST": "smtp.example.com",
		"MAIL_FROM": "status@example.com",
		"MAIL_TO":   "leon@example.com",
	}

	tests := []struct {
		name string
		env  map[string]string
		key  string
	}{
		{"addr ohne port", map[string]string{"ADDR": "localhost"}, "ADDR"},
		{"bool", map[string]string{"COOKIE_SECURE": "ja"}, "COOKIE_SECURE"},
		{"retention 0", map[string]string{"RETENTION_DAYS": "0"}, "RETENTION_DAYS"},
		{"retention text", map[string]string{"RETENTION_DAYS": "dreissig"}, "RETENTION_DAYS"},
		{"smtp port", map[string]string{"SMTP_PORT": "70000"}, "SMTP_PORT"},
		{"base url ohne schema", map[string]string{"BASE_URL": "status.example.com"}, "BASE_URL"},
		{"base url mit pfad", map[string]string{"BASE_URL": "https://example.com/status"}, "BASE_URL"},
		{"base url javascript", map[string]string{"BASE_URL": "javascript:alert(1)"}, "BASE_URL"},
		{"trust proxy alt", map[string]string{"TRUST_PROXY": "true"}, "TRUST_PROXY"},
		{"trusted proxy ohne laenge", map[string]string{"TRUSTED_PROXY": "172.20.0.1"}, "TRUSTED_PROXY"},
		{"trusted proxy kaputt", map[string]string{"TRUSTED_PROXY": "caddy"}, "TRUSTED_PROXY"},
		{"allow private alt", map[string]string{"ALLOW_PRIVATE": "true"}, "ALLOW_PRIVATE"},
		{"private allow ohne port", map[string]string{"PRIVATE_ALLOW": "proxy"}, "PRIVATE_ALLOW"},
		{"private allow port 0", map[string]string{"PRIVATE_ALLOW": "proxy:443,proxy:0"}, "PRIVATE_ALLOW"},
		{"private allow port zu gross", map[string]string{"PRIVATE_ALLOW": "proxy:65536"}, "PRIVATE_ALLOW"},
		{"private allow ohne host", map[string]string{"PRIVATE_ALLOW": ":443"}, "PRIVATE_ALLOW"},
		{"private allow port text", map[string]string{"PRIVATE_ALLOW": "proxy:https"}, "PRIVATE_ALLOW"},
		{"metrics token kurz", map[string]string{"METRICS_TOKEN": "abc"}, "METRICS_TOKEN"},
		{"mail ohne host", map[string]string{"MAIL_TO": "leon@example.com"}, "SMTP_HOST"},
		{"mail ohne from", with(mail, "MAIL_FROM", ""), "MAIL_FROM"},
		{"mail from kaputt", with(mail, "MAIL_FROM", "status@example.com\r\nBcc: x@example.com"), "MAIL_FROM"},
		{"mail to kaputt", with(mail, "MAIL_TO", "keine adresse"), "MAIL_TO"},
		{"smtp user ohne pass", with(mail, "SMTP_USER", "status"), "SMTP_USER"},
		{"admin ohne passwort", map[string]string{"ADMIN_USER": "leon"}, "ADMIN_USER"},
		{"admin passwort kurz", map[string]string{"ADMIN_USER": "leon", "ADMIN_PASSWORD": "kurz"}, "ADMIN_PASSWORD"},
		{"admin mit leerzeichen", map[string]string{"ADMIN_USER": "le on", "ADMIN_PASSWORD": "lang genug fuer den test"}, "ADMIN_USER"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(fromMap(tt.env))
			if err == nil || !strings.HasPrefix(err.Error(), tt.key+": ") {
				t.Fatalf("got %v, want fehler zu %s", err, tt.key)
			}
		})
	}
}

func TestMailOptional(t *testing.T) {
	// so steht es in deploy/env.example, solange MAIL_TO leer ist
	c, err := load(fromMap(map[string]string{
		"SMTP_HOST": "smtp.example.com",
		"SMTP_USER": "resend",
		"MAIL_FROM": "noreply@example.com",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.MailEnabled() {
		t.Fatal("ohne MAIL_TO keine mails")
	}
}

func TestAllErrors(t *testing.T) {
	_, err := load(fromMap(map[string]string{"ADDR": "x", "COOKIE_SECURE": "vielleicht"}))
	if err == nil || strings.Count(err.Error(), "\n") != 1 {
		t.Fatalf("beide fehler erwartet: %v", err)
	}
}

func with(m map[string]string, k, v string) map[string]string {
	out := map[string]string{k: v}
	for key, val := range m {
		if key != k {
			out[key] = val
		}
	}
	return out
}
