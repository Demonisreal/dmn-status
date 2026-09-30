package alert

import (
	"bytes"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
	"unicode"

	_ "time/tzdata"
)

var berlin, _ = time.LoadLocation("Europe/Berlin")

func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

func clock(t time.Time) string {
	return t.In(berlin).Format("02.01.2006, 15:04 Uhr")
}

func dauer(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d Sek.", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d Min.", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d Std. %d Min.", int(d.Hours()), int(d.Minutes())%60)
	}
	days := int(d.Hours()) / 24
	unit := "Tage"
	if days == 1 {
		unit = "Tag"
	}
	return fmt.Sprintf("%d %s %d Std.", days, unit, int(d.Hours())%24)
}

func (m *Mailer) downText(name, cause string, since time.Time) (subject, body string) {
	name = clean(name)
	subject = "[dmn-status] nicht erreichbar: " + name
	body = fmt.Sprintf("%s ist nicht erreichbar.\n\nSeit:    %s\nUrsache: %s\n", name, clock(since), clean(cause))
	return subject, body + m.link()
}

func (m *Mailer) upText(name string, from, to time.Time) (subject, body string) {
	name = clean(name)
	d := dauer(to.Sub(from))
	subject = "[dmn-status] wieder erreichbar: " + name + " nach " + d
	body = fmt.Sprintf("%s ist wieder erreichbar.\n\nAusfall: %s bis %s\nDauer:   %s\n", name, clock(from), clock(to), d)
	return subject, body + m.link()
}

func (m *Mailer) link() string {
	if m.BaseURL == "" {
		return ""
	}
	return "\n" + m.BaseURL + "/admin/\n"
}

func (m *Mailer) message(from *mail.Address, subject, body, id string, date time.Time) ([]byte, error) {
	domain := from.Address[strings.LastIndexByte(from.Address, '@')+1:]
	to := make([]string, len(m.To))
	for i, a := range m.To {
		to[i] = (&mail.Address{Address: a}).String()
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: %s\r\n", from)
	fmt.Fprintf(&buf, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&buf, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", clean(subject)))
	fmt.Fprintf(&buf, "Date: %s\r\n", date.In(berlin).Format(time.RFC1123Z))
	fmt.Fprintf(&buf, "Message-ID: <%s@%s>\r\n", id, domain)
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	buf.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")

	qp := quotedprintable.NewWriter(&buf)
	if _, err := qp.Write([]byte(body)); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
