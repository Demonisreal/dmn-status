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
	PrivateAllow []string

	Block []netip.Addr

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
