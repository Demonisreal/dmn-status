package check

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	base := Target{
		Name:          "Website",
		Kind:          KindHTTP,
		Address:       "https://example.com/",
		ExpectStatus:  "200-399",
		IntervalS:     60,
		TimeoutMs:     10000,
		FailThreshold: 3,
	}

	tests := []struct {
		name  string
		edit  func(*Target)
		field string // leer heisst gueltig
	}{
		{"gueltig", func(t *Target) {}, ""},
		{"http mit port und pfad", func(t *Target) { t.Address = "http://example.com:8080/health?x=1" }, ""},
		{"ipv6", func(t *Target) { t.Address = "http://[2001:db8::1]:8080/" }, ""},
		{"name leer", func(t *Target) { t.Name = "   " }, "name"},
		{"name zu lang", func(t *Target) { t.Name = strings.Repeat("a", 61) }, "name"},
		{"name 60 umlaute", func(t *Target) { t.Name = strings.Repeat("ü", 60) }, ""},
		{"name mit zeilenumbruch", func(t *Target) { t.Name = "a\r\nBcc: x" }, "name"},
		{"name rtl-override", func(t *Target) { t.Name = "web\u202Eetis" }, "name"},
		{"name nullbreite", func(t *Target) { t.Name = "web\u200Bsite" }, "name"},
		{"name isolate", func(t *Target) { t.Name = "\u2066web\u2069" }, "name"},
		{"art unbekannt", func(t *Target) { t.Kind = "icmp" }, "kind"},
		{"ftp", func(t *Target) { t.Address = "ftp://example.com/" }, "address"},
		{"ohne schema", func(t *Target) { t.Address = "example.com" }, "address"},
		{"userinfo", func(t *Target) { t.Address = "https://user:pw@example.com/" }, "address"},
		{"ohne host", func(t *Target) { t.Address = "https:///pfad" }, "address"},
		{"port 0", func(t *Target) { t.Address = "https://example.com:0/" }, "address"},
		{"port zu gross", func(t *Target) { t.Address = "https://example.com:70000/" }, "address"},
		{"host mit zone", func(t *Target) { t.Address = "http://[fe80::1%25eth0]/" }, "address"},
		{"statusliste", func(t *Target) { t.ExpectStatus = "200,401" }, ""},
		{"status kaputt", func(t *Target) { t.ExpectStatus = "2xx" }, "expect_status"},
		{"keyword 200", func(t *Target) { t.Keyword = strings.Repeat("k", 200) }, ""},
		{"keyword 201", func(t *Target) { t.Keyword = strings.Repeat("k", 201) }, "keyword"},
		{"connect_to", func(t *Target) { t.ConnectTo = "10.0.0.5:443" }, ""},
		{"connect_to ohne port", func(t *Target) { t.ConnectTo = "10.0.0.5" }, "connect_to"},
		{"intervall 29", func(t *Target) { t.IntervalS = 29 }, "interval_s"},
		{"intervall 30", func(t *Target) { t.IntervalS = 30; t.TimeoutMs = 29999 }, ""},
		{"timeout 499", func(t *Target) { t.TimeoutMs = 499 }, "timeout_ms"},
		{"timeout gleich intervall", func(t *Target) { t.TimeoutMs = 60000 }, "timeout_ms"},
		{"timeout 60000", func(t *Target) { t.IntervalS = 3600; t.TimeoutMs = 60000 }, ""},
		{"timeout 60001", func(t *Target) { t.IntervalS = 3600; t.TimeoutMs = 60001 }, "timeout_ms"},
		{"schwelle 0", func(t *Target) { t.FailThreshold = 0 }, "fail_threshold"},
		{"schwelle 20", func(t *Target) { t.FailThreshold = 20 }, ""},
		{"schwelle 21", func(t *Target) { t.FailThreshold = 21 }, "fail_threshold"},

		{"tcp", func(t *Target) { t.Kind = KindTCP; t.Address = "db.example.com:5432" }, ""},
		{"tcp url", func(t *Target) { t.Kind = KindTCP; t.Address = "https://example.com/" }, "address"},
		{"tcp port 0", func(t *Target) { t.Kind = KindTCP; t.Address = "example.com:0" }, "address"},
		{"tcp host leer", func(t *Target) { t.Kind = KindTCP; t.Address = ":22" }, "address"},
		{"tcp host kaputt", func(t *Target) { t.Kind = KindTCP; t.Address = "exa mple.com:22" }, "address"},
		{"tcp connect_to", func(t *Target) { t.Kind = KindTCP; t.Address = "example.com:22"; t.ConnectTo = "1.2.3.4:22" }, "connect_to"},
		{"tcp keyword", func(t *Target) { t.Kind = KindTCP; t.Address = "example.com:22"; t.Keyword = "ssh" }, "keyword"},
		{"fivem", func(t *Target) { t.Kind = KindFiveM; t.Address = "fx.example.com:30120" }, ""},
		{"fivem ipv6", func(t *Target) { t.Kind = KindFiveM; t.Address = "[2001:db8::1]:30120" }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tg := base
			tt.edit(&tg)
			errs := Validate(tg)
			if tt.field == "" {
				if len(errs) != 0 {
					t.Fatalf("soll gueltig sein, got %v", errs)
				}
				return
			}
			if _, ok := errs[tt.field]; !ok || len(errs) != 1 {
				t.Fatalf("erwarte nur fehler in %s, got %v", tt.field, errs)
			}
		})
	}

	ctrl := []struct {
		name  string
		edit  func(*Target)
		field string
	}{
		{"name zu lang und nullbreite", func(t *Target) { t.Name = strings.Repeat("a", 60) + "\u200B" }, "name"},
		{"address rtl-override im pfad", func(t *Target) { t.Address = "https://example.com/\u202Egnp.exe" }, "address"},
		{"tcp address nullbreite", func(t *Target) { t.Kind = KindTCP; t.Address = "exa\u200Bmple.com:22" }, "address"},
		{"keyword rtl-override", func(t *Target) { t.Keyword = "ok\u202Ekaputt" }, "keyword"},
		{"keyword zeilenumbruch", func(t *Target) { t.Keyword = "ok\n" }, "keyword"},
		{"connect_to isolate", func(t *Target) { t.ConnectTo = "\u2066proxy:443" }, "connect_to"},
	}
	for _, tt := range ctrl {
		t.Run(tt.name, func(t *testing.T) {
			tg := base
			tt.edit(&tg)
			errs := Validate(tg)
			if msg := errs[tt.field]; msg != "Keine Steuer- oder Formatzeichen" || len(errs) != 1 {
				t.Fatalf("erwarte nur steuerzeichen-fehler in %s, got %v", tt.field, errs)
			}
		})
	}
}
