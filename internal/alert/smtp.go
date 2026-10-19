package alert

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"sync"
	"time"
)

var ErrLimit = errors.New("mail-limit erreicht")

const (
	perHour     = 20
	perHourTest = 3
	dialTimeout = 15 * time.Second
	sendTimeout = 30 * time.Second
)

type Mailer struct {
	Host    string
	Port    int
	User    string
	Pass    string
	From    string
	To      []string
	BaseURL string
	TLS     *tls.Config
	Now     func() time.Time

	mu       sync.Mutex
	sent     []time.Time
	sentTest []time.Time
}

func (m *Mailer) Down(ctx context.Context, name, cause string, since time.Time) error {
	subject, body := m.downText(name, cause, since)
	return m.send(ctx, false, subject, body)
}

func (m *Mailer) Up(ctx context.Context, name string, from, to time.Time) error {
	subject, body := m.upText(name, from, to)
	return m.send(ctx, false, subject, body)
}

func (m *Mailer) Test(ctx context.Context) error {
	return m.send(ctx, true, "[dmn-status] Testmail", "Mailversand von dmn-status funktioniert.\n")
}

func (m *Mailer) reserve(test bool) error {
	now := m.Now()
	m.mu.Lock()
	defer m.mu.Unlock()

	sent, limit := &m.sent, perHour
	if test {
		sent, limit = &m.sentTest, perHourTest
	}
	cut := 0
	for cut < len(*sent) && !(*sent)[cut].After(now.Add(-time.Hour)) {
		cut++
	}
	*sent = (*sent)[cut:]
	if len(*sent) >= limit {
		return ErrLimit
	}
	*sent = append(*sent, now)
	return nil
}

func (m *Mailer) send(ctx context.Context, test bool, subject, body string) error {
	from, err := mail.ParseAddress(m.From)
	if err != nil {
		return err
	}
	msg, err := m.message(from, subject, body, rand.Text(), m.Now())
	if err != nil {
		return err
	}
	if err := m.reserve(test); err != nil {
		return err
	}

	cfg := &tls.Config{}
	if m.TLS != nil {
		cfg = m.TLS.Clone()
	}
	cfg.ServerName = m.Host
	cfg.MinVersion = tls.VersionTLS12
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: dialTimeout}, Config: cfg}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(m.Host, strconv.Itoa(m.Port)))
	if err != nil {
		return err
	}
	if err := conn.SetDeadline(time.Now().Add(sendTimeout)); err != nil {
		conn.Close()
		return err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()

	if m.User != "" {
		if err := c.Auth(smtp.PlainAuth("", m.User, m.Pass, m.Host)); err != nil {
			return err
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return err
	}
	for _, to := range m.To {
		if err := c.Rcpt(to); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	_ = c.Quit()
	return nil
}
