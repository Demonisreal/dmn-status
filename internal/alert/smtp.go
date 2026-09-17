// Package alert verschickt die Mails bei Ausfall und Wiederkehr. Nur implizites TLS (Port 465):
// ein server ohne TLS scheitert schon beim handshake, klartext gibt es nicht.
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
	perHour = 20
	// eigenes budget, oft gedrueckte testmails sollen keine ausfall-mails verdraengen
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
	BaseURL string      // fuer den link in der mail, darf leer sein
	TLS     *tls.Config // nil heisst systemzertifikate, im test nur RootCAs
	Now     func() time.Time

	mu       sync.Mutex
	sent     []time.Time
	sentTest []time.Time
}

// Down meldet einen Ausfall seit since.
func (m *Mailer) Down(ctx context.Context, name, cause string, since time.Time) error {
	subject, body := m.downText(name, cause, since)
	return m.send(ctx, false, subject, body)
}

// Up meldet die Wiederkehr nach einem Ausfall von from bis to.
func (m *Mailer) Up(ctx context.Context, name string, from, to time.Time) error {
	subject, body := m.upText(name, from, to)
	return m.send(ctx, false, subject, body)
}

func (m *Mailer) Test(ctx context.Context) error {
	return m.send(ctx, true, "[dmn-status] Testmail", "Mailversand von dmn-status funktioniert.\n")
}

// reserve zaehlt versuche, nicht zugestellte mails. Ein haengender server soll nicht dazu
// fuehren, dass bei jedem check neu verbunden wird.
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
	// die frist gilt fuer die ganze sitzung, net/smtp selbst kennt keine timeouts.
	// echte uhr, Now kann im test stehen.
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
	// nach dem 250 auf DATA ist die mail angenommen, ein fehler beim QUIT aendert daran nichts
	_ = c.Quit()
	return nil
}
