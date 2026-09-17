package check

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"time"
)

const maxBody = 1 << 20

var errStatus = errors.New("status ungueltig")

func (c Checker) checkHTTP(ctx context.Context, t Target) Result {
	ranges, err := parseStatus(t.ExpectStatus)
	if err != nil {
		return Result{Err: "statusangabe ungültig"}
	}

	start := time.Now()
	var first time.Time
	trace := &httptrace.ClientTrace{
		GotFirstResponseByte: func() { first = time.Now() },
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, t.Address, nil)
	if err != nil {
		return Result{Err: "adresse ungültig"}
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.client(t.ConnectTo).Do(req)
	if err != nil {
		return Result{LatencyMs: since(start), Err: errText(err)}
	}
	defer resp.Body.Close()

	res := Result{
		LatencyMs:  int(first.Sub(start).Milliseconds()),
		StatusCode: resp.StatusCode,
	}
	if !statusOK(ranges, resp.StatusCode) {
		res.Err = fmt.Sprintf("status %d", resp.StatusCode)
		return res
	}
	if t.Keyword != "" {
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		if err != nil {
			res.Err = errText(err)
			return res
		}
		if !bytes.Contains(body, []byte(t.Keyword)) {
			res.Err = "stichwort fehlt"
			return res
		}
	}
	res.OK = true
	return res
}

// client baut pro Check einen eigenen Transport ohne Keep-Alive. Jede Messung enthaelt so
// Verbindungsaufbau und TLS, und connect_to kann pro Ziel den Dial umbiegen.
func (c Checker) client(connectTo string) *http.Client {
	dial := dialer(c.loopback, nil, c.Block).DialContext
	if connectTo != "" {
		// url, sni und host-header bleiben, nur die tcp-verbindung geht woanders hin
		dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return c.dialConnectTo(ctx, network, connectTo)
		}
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext:            dial,
			DisableKeepAlives:      true,
			ForceAttemptHTTP2:      true,
			MaxResponseHeaderBytes: 64 << 10,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type statusRange struct{ lo, hi int }

// parseStatus versteht "200-399", "200,401" und Mischformen wie "200-299,401".
func parseStatus(s string) ([]statusRange, error) {
	var out []statusRange
	for part := range strings.SplitSeq(s, ",") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(part), "-")
		a, err := strconv.Atoi(strings.TrimSpace(lo))
		b := a
		if err == nil && isRange {
			b, err = strconv.Atoi(strings.TrimSpace(hi))
		}
		if err != nil || a < 100 || b > 599 || a > b {
			return nil, errStatus
		}
		out = append(out, statusRange{a, b})
	}
	return out, nil
}

func statusOK(ranges []statusRange, code int) bool {
	for _, r := range ranges {
		if code >= r.lo && code <= r.hi {
			return true
		}
	}
	return false
}
