package check

import (
	"context"
	"time"
)

func (c Checker) checkTCP(ctx context.Context, t Target) Result {
	start := time.Now()
	conn, err := dialer(c.loopback, nil, c.Block).DialContext(ctx, "tcp", t.Address)
	if err != nil {
		return Result{LatencyMs: since(start), Err: errText(err)}
	}
	res := Result{OK: true, LatencyMs: since(start)}
	conn.Close()
	return res
}
