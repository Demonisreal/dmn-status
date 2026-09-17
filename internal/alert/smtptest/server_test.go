package smtptest

import (
	"crypto/tls"
	"net"
	"net/smtp"
	"strings"
	"testing"
	"time"
)

func dial(t *testing.T, s *Server) *smtp.Client {
	t.Helper()
	host, _, _ := net.SplitHostPort(s.Addr())
	conn, err := tls.Dial("tcp", s.Addr(), &tls.Config{RootCAs: s.CertPool(), ServerName: host})
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func send(c *smtp.Client, from, to, body string) error {
	if err := c.Mail(from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(body)); err != nil {
		return err
	}
	return w.Close()
}

func TestSendStoresMessage(t *testing.T) {
	s := Start(t, "resend", "key")
	host, _, _ := net.SplitHostPort(s.Addr())
	c := dial(t, s)

	if err := c.Auth(smtp.PlainAuth("", "resend", "key", host)); err != nil {
		t.Fatalf("auth: %v", err)
	}
	body := "Subject: Ziel down\r\n\r\nerste zeile\r\n.punkt am anfang\r\n"
	if err := send(c, "status@example.test", "admin@example.test", body); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := c.Quit(); err != nil {
		t.Fatalf("quit: %v", err)
	}

	msgs := s.Wait(t, 1, time.Second)
	m := msgs[0]
	if m.From != "status@example.test" {
		t.Errorf("From = %q", m.From)
	}
	if len(m.To) != 1 || m.To[0] != "admin@example.test" {
		t.Errorf("To = %q", m.To)
	}
	want := "Subject: Ziel down\n\nerste zeile\n.punkt am anfang\n"
	if string(m.Data) != want {
		t.Errorf("Data = %q, want %q", m.Data, want)
	}
}

func TestWrongPasswordFailsAuth(t *testing.T) {
	s := Start(t, "resend", "key")
	host, _, _ := net.SplitHostPort(s.Addr())
	c := dial(t, s)

	err := c.Auth(smtp.PlainAuth("", "resend", "falsch", host))
	if err == nil || !strings.HasPrefix(err.Error(), "535") {
		t.Fatalf("auth err = %v, want 535", err)
	}
}

func TestMailWithoutAuthRejected(t *testing.T) {
	s := Start(t, "resend", "key")
	c := dial(t, s)

	if err := c.Mail("status@example.test"); err == nil {
		t.Fatal("MAIL ohne AUTH ging durch")
	}
}

func TestFailDataThenRetry(t *testing.T) {
	s := Start(t, "resend", "key")
	host, _, _ := net.SplitHostPort(s.Addr())
	s.FailData(1)
	c := dial(t, s)
	if err := c.Auth(smtp.PlainAuth("", "resend", "key", host)); err != nil {
		t.Fatalf("auth: %v", err)
	}

	err := send(c, "status@example.test", "admin@example.test", "Subject: a\r\n\r\nx\r\n")
	if err == nil || !strings.HasPrefix(err.Error(), "451") {
		t.Fatalf("erster versuch err = %v, want 451", err)
	}
	if n := len(s.Messages()); n != 0 {
		t.Fatalf("nach 451 gespeichert: %d", n)
	}

	if err := send(c, "status@example.test", "admin@example.test", "Subject: a\r\n\r\nx\r\n"); err != nil {
		t.Fatalf("zweiter versuch: %v", err)
	}
	s.Wait(t, 1, time.Second)
}

func TestSeveralMailsOnOneConnection(t *testing.T) {
	s := Start(t, "resend", "key")
	host, _, _ := net.SplitHostPort(s.Addr())
	c := dial(t, s)

	if err := c.Auth(smtp.PlainAuth("", "resend", "key", host)); err != nil {
		t.Fatalf("auth: %v", err)
	}
	for range 3 {
		if err := send(c, "status@example.test", "admin@example.test", "Subject: a\r\n\r\nx\r\n"); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	if got := len(s.Wait(t, 3, time.Second)); got != 3 {
		t.Fatalf("mails = %d, want 3", got)
	}
}
