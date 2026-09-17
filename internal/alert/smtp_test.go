package alert

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Demonisreal/dmn-status/internal/alert/smtptest"
	"github.com/Demonisreal/dmn-status/internal/testutil"
)

var update = flag.Bool("update", false, "golden-dateien neu schreiben")

// 14:03 in Berlin (MESZ)
var start = time.Date(2026, 9, 17, 12, 3, 0, 0, time.UTC)

func newMailer(t *testing.T, s *smtptest.Server, clock *testutil.Clock) *Mailer {
	t.Helper()
	host, port, _ := net.SplitHostPort(s.Addr())
	p, _ := strconv.Atoi(port)
	return &Mailer{
		Host:    host,
		Port:    p,
		User:    "resend",
		Pass:    "key",
		From:    "dmn-status <noreply@example.test>",
		To:      []string{"admin@example.test", "zweit@example.test"},
		BaseURL: "https://status.example.test",
		TLS:     &tls.Config{RootCAs: s.CertPool()},
		Now:     clock.Now,
	}
}

type parsed struct {
	header mail.Header
	body   string
}

func parse(t *testing.T, data []byte) parsed {
	t.Helper()
	msg, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("nachricht: %v", err)
	}
	body, err := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if err != nil {
		t.Fatalf("body: %v", err)
	}
	return parsed{msg.Header, string(body)}
}

func (p parsed) subject(t *testing.T) string {
	t.Helper()
	s, err := new(mime.WordDecoder).DecodeHeader(p.header.Get("Subject"))
	if err != nil {
		t.Fatalf("subject: %v", err)
	}
	return s
}

func TestDownAndUp(t *testing.T) {
	s := smtptest.Start(t, "resend", "key")
	clock := testutil.NewClock(start)
	m := newMailer(t, s, clock)
	ctx := context.Background()

	if err := m.Down(ctx, "Bewerbungsportal", "zeitueberschreitung", start); err != nil {
		t.Fatal(err)
	}
	if err := m.Up(ctx, "Bewerbungsportal", start, start.Add(18*time.Minute+40*time.Second)); err != nil {
		t.Fatal(err)
	}
	msgs := s.Wait(t, 2, 5*time.Second)

	if msgs[0].From != "noreply@example.test" || strings.Join(msgs[0].To, ",") != "admin@example.test,zweit@example.test" {
		t.Errorf("umschlag %q %q", msgs[0].From, msgs[0].To)
	}

	d := parse(t, msgs[0].Data)
	checks := map[string]string{
		"From":                      `"dmn-status" <noreply@example.test>`,
		"To":                        "<admin@example.test>, <zweit@example.test>",
		"Date":                      "Thu, 17 Sep 2026 14:03:00 +0200",
		"Mime-Version":              "1.0",
		"Content-Type":              "text/plain; charset=utf-8",
		"Content-Transfer-Encoding": "quoted-printable",
	}
	for k, want := range checks {
		if got := d.header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if id := d.header.Get("Message-Id"); !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, "@example.test>") || len(id) < 30 {
		t.Errorf("Message-ID %q", id)
	}
	if got := d.subject(t); got != "[dmn-status] nicht erreichbar: Bewerbungsportal" {
		t.Errorf("subject %q", got)
	}
	for _, want := range []string{"17.09.2026, 14:03 Uhr", "zeitueberschreitung", "https://status.example.test/admin/"} {
		if !strings.Contains(d.body, want) {
			t.Errorf("body ohne %q:\n%s", want, d.body)
		}
	}

	u := parse(t, msgs[1].Data)
	if got := u.subject(t); got != "[dmn-status] wieder erreichbar: Bewerbungsportal nach 18 Min." {
		t.Errorf("subject %q", got)
	}
	if u.header.Get("Message-Id") == d.header.Get("Message-Id") {
		t.Error("Message-ID doppelt")
	}
}

func TestHeaderInjection(t *testing.T) {
	s := smtptest.Start(t, "resend", "key")
	m := newMailer(t, s, testutil.NewClock(start))

	name := "Web\r\nBcc: fremd@example.test\r\n\r\nText\u202e"
	if err := m.Down(context.Background(), name, "status 503\r\nX-Evil: 1", start); err != nil {
		t.Fatal(err)
	}
	msg := s.Wait(t, 1, 5*time.Second)[0]
	for _, to := range msg.To {
		if strings.Contains(to, "fremd") {
			t.Fatalf("empfaenger eingeschleust: %q", msg.To)
		}
	}
	p := parse(t, msg.Data)
	if p.header.Get("Bcc") != "" || p.header.Get("X-Evil") != "" {
		t.Errorf("header eingeschleust: %v", p.header)
	}
	if got := p.subject(t); got != "[dmn-status] nicht erreichbar: WebBcc: fremd@example.testText" {
		t.Errorf("subject %q", got)
	}
}

func TestLimit(t *testing.T) {
	s := smtptest.Start(t, "resend", "key")
	clock := testutil.NewClock(start)
	m := newMailer(t, s, clock)
	ctx := context.Background()

	for i := range perHour {
		if err := m.Down(ctx, "Web", "timeout", start); err != nil {
			t.Fatalf("mail %d: %v", i+1, err)
		}
		clock.Advance(time.Minute)
	}
	if err := m.Down(ctx, "Web", "timeout", start); !errors.Is(err, ErrLimit) {
		t.Fatalf("mail 21: %v, want ErrLimit", err)
	}
	if n := len(s.Messages()); n != perHour {
		t.Fatalf("%d mails angekommen", n)
	}

	// die erste faellt nach einer stunde aus dem fenster, die zweite noch nicht
	clock.Set(start.Add(time.Hour))
	if err := m.Up(ctx, "Web", start, clock.Now()); err != nil {
		t.Fatalf("nach einer stunde: %v", err)
	}
	if err := m.Up(ctx, "Web", start, clock.Now()); !errors.Is(err, ErrLimit) {
		t.Fatalf("fenster gleitet nicht: %v", err)
	}
}

func TestTestMailLimit(t *testing.T) {
	s := smtptest.Start(t, "resend", "key")
	clock := testutil.NewClock(start)
	m := newMailer(t, s, clock)
	ctx := context.Background()

	for i := range perHourTest {
		if err := m.Test(ctx); err != nil {
			t.Fatalf("testmail %d: %v", i+1, err)
		}
	}
	if err := m.Test(ctx); !errors.Is(err, ErrLimit) {
		t.Fatalf("testmail %d: %v, want ErrLimit", perHourTest+1, err)
	}
	// das eigene budget ist aufgebraucht, ausfall-mails gehen trotzdem raus
	if err := m.Down(ctx, "Web", "timeout", start); err != nil {
		t.Fatalf("ausfall-mail nach testmails: %v", err)
	}
	if n := len(s.Messages()); n != perHourTest+1 {
		t.Errorf("%d mails angekommen", n)
	}

	clock.Advance(time.Hour)
	if err := m.Test(ctx); err != nil {
		t.Errorf("testmail nach einer stunde: %v", err)
	}
}

func TestSendErrors(t *testing.T) {
	s := smtptest.Start(t, "resend", "key")
	ctx := context.Background()

	s.FailData(1)
	m := newMailer(t, s, testutil.NewClock(start))
	if err := m.Test(ctx); err == nil || !strings.HasPrefix(err.Error(), "451") {
		t.Fatalf("451 nicht gemeldet: %v", err)
	}

	m.Pass = "falsch"
	if err := m.Test(ctx); err == nil || !strings.HasPrefix(err.Error(), "535") {
		t.Fatalf("falsches passwort: %v", err)
	}

	m = newMailer(t, s, testutil.NewClock(start))
	m.TLS = nil
	if err := m.Test(ctx); err == nil {
		t.Fatal("selbst signiertes zertifikat akzeptiert")
	}
	if n := len(s.Messages()); n != 0 {
		t.Errorf("%d mails gespeichert", n)
	}
}

func TestSendCanceled(t *testing.T) {
	s := smtptest.Start(t, "resend", "key")
	m := newMailer(t, s, testutil.NewClock(start))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Test(ctx); err == nil {
		t.Fatal("abgebrochener context ohne fehler")
	}
}

func TestGolden(t *testing.T) {
	m := &Mailer{
		From:    "dmn-status <noreply@dmn-software.example>",
		To:      []string{"admin@example.test"},
		BaseURL: "https://status.example.test",
	}
	from, err := mail.ParseAddress(m.From)
	if err != nil {
		t.Fatal(err)
	}
	ended := start.Add(26*time.Hour + 5*time.Minute)

	downSubject, downBody := m.downText("Bewerbungsportal (Größe)", "verbindung abgelehnt", start)
	upSubject, upBody := m.upText("Bewerbungsportal (Größe)", start, ended)
	tests := []struct {
		file          string
		subject, body string
	}{
		{"down.golden", downSubject, downBody},
		{"up.golden", upSubject, upBody},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			got, err := m.message(from, tt.subject, tt.body, "FESTEID", ended)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(bytes.ReplaceAll(got, []byte("\r\n"), nil), []byte("\n")) {
				t.Error("zeilenende ohne CR")
			}
			// golden-dateien mit \n, sonst dreht git unter windows an den zeilenenden
			got = bytes.ReplaceAll(got, []byte("\r\n"), []byte("\n"))

			path := filepath.Join("testdata", tt.file)
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("abweichung von %s:\n%s", path, got)
			}
		})
	}
}

func TestDauer(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{45 * time.Second, "45 Sek."},
		{time.Minute, "1 Min."},
		{59*time.Minute + 59*time.Second, "59 Min."},
		{2*time.Hour + 14*time.Minute, "2 Std. 14 Min."},
		{24 * time.Hour, "1 Tag 0 Std."},
		{50 * time.Hour, "2 Tage 2 Std."},
	}
	for _, tt := range tests {
		if got := dauer(tt.d); got != tt.want {
			t.Errorf("dauer(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}
