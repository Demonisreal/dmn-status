// Package smtptest startet einen minimalen SMTP-Server mit implizitem TLS fuer Tests.
// Er versteht nur das, was net/smtp fuer AUTH PLAIN und eine Mail braucht.
package smtptest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"
)

type Message struct {
	From string
	To   []string
	Data []byte // zeilenenden als \n, punkte schon entstopft
}

type Server struct {
	user, pass string

	ln   net.Listener
	pool *x509.CertPool
	wg   sync.WaitGroup

	mu       sync.Mutex
	msgs     []Message
	conns    map[net.Conn]struct{}
	failData int
	changed  chan struct{}
}

// Start lauscht auf 127.0.0.1 mit zufaelligem Port. Aufgeraeumt wird ueber t.Cleanup.
func Start(t testing.TB, user, pass string) *Server {
	t.Helper()

	cert, pool := selfSigned(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("smtptest: listen: %v", err)
	}

	s := &Server{
		user:    user,
		pass:    pass,
		ln:      ln,
		pool:    pool,
		conns:   map[net.Conn]struct{}{},
		changed: make(chan struct{}, 1),
	}
	s.wg.Add(1)
	go s.accept()

	t.Cleanup(func() {
		ln.Close()
		s.mu.Lock()
		for c := range s.conns {
			c.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
	return s
}

func (s *Server) Addr() string { return s.ln.Addr().String() }

// CertPool enthaelt nur das selbst signierte Zertifikat, gedacht fuer tls.Config.RootCAs.
func (s *Server) CertPool() *x509.CertPool { return s.pool }

// FailData laesst die naechsten n Mails nach dem abschliessenden Punkt mit 451 scheitern.
// Der Inhalt ist dann schon uebertragen, wird aber nicht gespeichert.
func (s *Server) FailData(n int) {
	s.mu.Lock()
	s.failData = n
	s.mu.Unlock()
}

func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.msgs...)
}

// Wait blockiert, bis mindestens n Mails angekommen sind, und bricht den Test sonst ab.
func (s *Server) Wait(t testing.TB, n int, timeout time.Duration) []Message {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		if msgs := s.Messages(); len(msgs) >= n {
			return msgs
		}
		select {
		case <-s.changed:
		case <-deadline.C:
			t.Fatalf("smtptest: %d von %d mails nach %v", len(s.Messages()), n, timeout)
		}
	}
}

func (s *Server) accept() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns[c] = struct{}{}
		s.mu.Unlock()

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serve(c)
			s.mu.Lock()
			delete(s.conns, c)
			s.mu.Unlock()
			c.Close()
		}()
	}
}

func (s *Server) serve(c net.Conn) {
	tc := textproto.NewConn(c)
	tc.PrintfLine("220 smtptest ESMTP")

	var (
		authed bool
		msg    *Message
	)
	for {
		line, err := tc.ReadLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")

		switch strings.ToUpper(verb) {
		case "EHLO":
			tc.PrintfLine("250-smtptest")
			tc.PrintfLine("250 AUTH PLAIN")
		case "HELO":
			tc.PrintfLine("250 smtptest")
		case "AUTH":
			mech, resp, _ := strings.Cut(arg, " ")
			if !strings.EqualFold(mech, "PLAIN") {
				tc.PrintfLine("504 nur PLAIN")
				continue
			}
			if resp == "" {
				tc.PrintfLine("334 ")
				if resp, err = tc.ReadLine(); err != nil {
					return
				}
			}
			if s.checkPlain(resp) {
				authed = true
				tc.PrintfLine("235 ok")
			} else {
				tc.PrintfLine("535 falsche zugangsdaten")
			}
		case "MAIL":
			if !authed {
				tc.PrintfLine("530 erst anmelden")
				continue
			}
			msg = &Message{From: addr(arg)}
			tc.PrintfLine("250 ok")
		case "RCPT":
			if msg == nil {
				tc.PrintfLine("503 erst MAIL")
				continue
			}
			msg.To = append(msg.To, addr(arg))
			tc.PrintfLine("250 ok")
		case "DATA":
			if msg == nil || len(msg.To) == 0 {
				tc.PrintfLine("503 erst MAIL und RCPT")
				continue
			}
			tc.PrintfLine("354 ende mit <CRLF>.<CRLF>")
			data, err := tc.ReadDotBytes()
			if err != nil {
				return
			}
			msg.Data = data
			if s.store(*msg) {
				tc.PrintfLine("250 ok")
			} else {
				tc.PrintfLine("451 voruebergehend nicht moeglich")
			}
			msg = nil
		case "RSET":
			msg = nil
			tc.PrintfLine("250 ok")
		case "NOOP":
			tc.PrintfLine("250 ok")
		case "QUIT":
			tc.PrintfLine("221 bye")
			return
		default:
			tc.PrintfLine("502 unbekannt")
		}
	}
}

func (s *Server) checkPlain(resp string) bool {
	raw, err := base64.StdEncoding.DecodeString(resp)
	if err != nil {
		return false
	}
	// authzid \0 authcid \0 passwort, authzid darf leer sein
	parts := bytes.Split(raw, []byte{0})
	return len(parts) == 3 && string(parts[1]) == s.user && string(parts[2]) == s.pass
}

func (s *Server) store(m Message) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failData > 0 {
		s.failData--
		return false
	}
	s.msgs = append(s.msgs, m)
	select {
	case s.changed <- struct{}{}:
	default:
	}
	return true
}

// addr holt die adresse aus "FROM:<a@b>" bzw. "TO:<a@b>", parameter dahinter fallen weg
func addr(arg string) string {
	_, rest, _ := strings.Cut(arg, "<")
	a, _, _ := strings.Cut(rest, ">")
	return a
}

func selfSigned(t testing.TB) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("smtptest: key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "smtptest"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("smtptest: cert: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("smtptest: cert: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}
