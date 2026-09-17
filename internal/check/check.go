// Package check fuehrt die einzelnen Pruefungen aus: http, tcp und fivem.
package check

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"time"
)

const (
	KindHTTP  = "http"
	KindTCP   = "tcp"
	KindFiveM = "fivem"
)

const userAgent = "dmn-status"

type Target struct {
	ID            int64
	Name          string
	Kind          string
	Address       string
	ExpectStatus  string
	Keyword       string
	ConnectTo     string
	IntervalS     int
	TimeoutMs     int
	FailThreshold int
	Public        bool
	Paused        bool
	Sort          int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Result ist das Ergebnis eines Checks. Err ist kurz und landet so im Admin, deshalb ohne
// Adressen oder Details aus dem Netzwerkstack.
type Result struct {
	OK         bool
	LatencyMs  int
	StatusCode int
	Err        string
	Players    *int
	MaxPlayers *int
	Version    string
}

type Checker struct {
	// PrivateAllow sind host:port-Eintraege, die connect_to trotz privater Adresse
	// erreichen darf. Die address selbst muss immer oeffentlich sein, loopback bleibt zu.
	PrivateAllow []string

	// Block sind aufgeloeste eigene oeffentliche Adressen, z. B. aus BASE_URL, um Pruefungen
	// der eigenen Statusseite gegen sich selbst zu verhindern.
	Block []netip.Addr

	// nur fuer tests im paket: httptest lauscht auf 127.0.0.1, lookup ersetzt das DNS
	loopback bool
	lookup   func(ctx context.Context, host string) ([]netip.Addr, error)
}

func (c Checker) Run(ctx context.Context, t Target) Result {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(t.TimeoutMs)*time.Millisecond)
	defer cancel()

	switch t.Kind {
	case KindHTTP:
		return c.checkHTTP(ctx, t)
	case KindTCP:
		return c.checkTCP(ctx, t)
	case KindFiveM:
		return c.checkFiveM(ctx, t)
	}
	return Result{Err: "unbekannte art"}
}

func since(start time.Time) int {
	return int(time.Since(start).Milliseconds())
}

func errText(err error) string {
	var (
		netErr  net.Error
		dnsErr  *net.DNSError
		certErr *tls.CertificateVerificationError
		synErr  *json.SyntaxError
		typeErr *json.UnmarshalTypeError
	)
	switch {
	case errors.Is(err, errBlocked):
		return "ziel gesperrt"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "zeitüberschreitung"
	case errors.Is(err, errRefused):
		return "verbindung abgelehnt"
	case errors.As(err, &dnsErr):
		return "name nicht auflösbar"
	case errors.As(err, &certErr):
		return "zertifikat ungültig"
	case errors.As(err, &synErr), errors.As(err, &typeErr), errors.Is(err, io.ErrUnexpectedEOF):
		return "antwort ungültig"
	}
	return "verbindungsfehler"
}
