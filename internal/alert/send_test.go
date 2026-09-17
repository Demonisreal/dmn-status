package alert

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Demonisreal/dmn-status/internal/alert/smtptest"
	"github.com/Demonisreal/dmn-status/internal/testutil"
)

func TestLimitCountsFailedAttempts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	ln.Close()

	m := &Mailer{
		Host: "127.0.0.1",
		Port: addr.Port,
		From: "noreply@example.test",
		To:   []string{"admin@example.test"},
		Now:  testutil.NewClock(start).Now,
	}
	ctx := context.Background()
	for i := range perHour {
		if err := m.Down(ctx, "Web", "timeout", start); err == nil || errors.Is(err, ErrLimit) {
			t.Fatalf("versuch %d: %v", i+1, err)
		}
	}
	// der 21. versuch waehlt gar nicht erst
	if err := m.Down(ctx, "Web", "timeout", start); !errors.Is(err, ErrLimit) {
		t.Errorf("21. versuch: %v", err)
	}
}

func TestBadFromKeepsLimit(t *testing.T) {
	s := smtptest.Start(t, "resend", "key")
	m := newMailer(t, s, testutil.NewClock(start))
	from := m.From
	m.From = "kein absender"
	for range perHour + 1 {
		if err := m.Down(context.Background(), "Web", "timeout", start); err == nil || errors.Is(err, ErrLimit) {
			t.Fatalf("absender kaputt: %v", err)
		}
	}
	m.From = from
	if err := m.Down(context.Background(), "Web", "timeout", start); err != nil {
		t.Errorf("gueltige mail danach: %v", err)
	}
}

// silentTLS nimmt verbindungen an, macht den handshake und schweigt dann
func silentTLS(t *testing.T) (addr string, pool *x509.CertPool) {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	srv.StartTLS()
	certs := srv.TLS.Certificates
	pool = x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	srv.Close()

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: certs})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		ln.Close()
		wg.Wait()
	})
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Go(func() {
				defer c.Close()
				_, _ = io.Copy(io.Discard, c)
			})
		}
	})
	return ln.Addr().String(), pool
}

func TestSendAbortsOnSilentServer(t *testing.T) {
	addr, pool := silentTLS(t)
	host, port, _ := net.SplitHostPort(addr)
	p, _ := strconv.Atoi(port)
	m := &Mailer{
		Host: host,
		Port: p,
		From: "noreply@example.test",
		To:   []string{"admin@example.test"},
		TLS:  &tls.Config{RootCAs: pool},
		Now:  testutil.NewClock(start).Now,
	}

	// net/smtp wartet ohne frist auf den gruss, nur der context bricht das ab
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	began := time.Now()
	err := m.Test(ctx)
	if err == nil {
		t.Fatal("schweigender server ohne fehler")
	}
	if d := time.Since(began); d > 3*time.Second {
		t.Errorf("abbruch erst nach %v", d)
	}
}

func TestSendRejectsUntrustedTLS(t *testing.T) {
	s := smtptest.Start(t, "resend", "key")
	_, otherPool := silentTLS(t)

	tests := []struct {
		name string
		cfg  *tls.Config
	}{
		{"systemzertifikate", nil},
		{"leere_config", &tls.Config{}},
		{"fremde_ca", &tls.Config{RootCAs: otherPool}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMailer(t, s, testutil.NewClock(start))
			m.TLS = tt.cfg
			if err := m.Test(context.Background()); err == nil {
				t.Error("verbindung akzeptiert")
			}
		})
	}
	if n := len(s.Messages()); n != 0 {
		t.Errorf("%d mails angekommen", n)
	}
}

// ein server ohne TLS bekommt weder AUTH noch passwort zu sehen
func TestSendNoPlaintext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- ""
			return
		}
		defer c.Close()
		_, _ = c.Write([]byte("220 klartext ESMTP\r\n"))
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		b, _ := io.ReadAll(c)
		got <- string(b)
	}()
	defer ln.Close()

	addr := ln.Addr().(*net.TCPAddr)
	m := &Mailer{
		Host: "127.0.0.1",
		Port: addr.Port,
		User: "resend",
		Pass: "geheimes-passwort",
		From: "noreply@example.test",
		To:   []string{"admin@example.test"},
		Now:  testutil.NewClock(start).Now,
	}
	if err := m.Test(context.Background()); err == nil {
		t.Fatal("klartext-server akzeptiert")
	}
	seen := <-got
	for _, secret := range []string{"AUTH", "EHLO", "geheimes-passwort", "cmVzZW5k"} {
		if strings.Contains(seen, secret) {
			t.Errorf("server sieht %q", secret)
		}
	}
}
