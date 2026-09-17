package check

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	fivemLimit = 256 << 10
	maxVersion = 64
)

type fivemInfo struct {
	Server string `json:"server"`
	Vars   struct {
		// FXServer liefert convars als String, aeltere Builds und Proxys teils als Zahl
		MaxClients json.RawMessage `json:"sv_maxClients"`
	} `json:"vars"`
}

// checkFiveM fragt info.json und players.json ab. Nur info.json entscheidet ueber den
// Zustand: players.json ist bei sv_requestParanoia oft gesperrt, der Server laeuft trotzdem.
func (c Checker) checkFiveM(ctx context.Context, t Target) Result {
	client := c.client(t.ConnectTo)
	base := "http://" + t.Address

	start := time.Now()
	var info fivemInfo
	status, err := getJSON(ctx, client, base+"/info.json", &info)
	res := Result{LatencyMs: since(start), StatusCode: status}
	switch {
	case errors.Is(err, errStatus):
		res.Err = fmt.Sprintf("status %d", status)
		return res
	case err != nil:
		res.Err = errText(err)
		return res
	case info.Server == "":
		res.Err = "antwort ungültig"
		return res
	}

	res.OK = true
	res.Version = version(info.Server)
	// fxserver erlaubt hoechstens 2048 slots, alles andere ist unsinn aus der antwort
	if n, err := strconv.Atoi(strings.Trim(string(info.Vars.MaxClients), `"`)); err == nil && n >= 0 && n <= 2048 {
		res.MaxPlayers = &n
	}

	// struct{} verwirft namen, identifier und endpoints schon beim dekodieren
	var players []struct{}
	if _, err := getJSON(ctx, client, base+"/players.json", &players); err == nil {
		n := len(players)
		res.Players = &n
	}
	return res
}

func getJSON(ctx context.Context, client *http.Client, url string, v any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, errStatus
	}
	return resp.StatusCode, json.NewDecoder(io.LimitReader(resp.Body, fivemLimit)).Decode(v)
}

func version(s string) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if hidden(r) {
			return -1
		}
		return r
	}, s))
	if r := []rune(s); len(r) > maxVersion {
		s = string(r[:maxVersion])
	}
	return s
}
