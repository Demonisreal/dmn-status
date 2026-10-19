package monitor

import "time"

type event int

const (
	none event = iota
	down
	up
)

type state struct {
	fails     int
	firstFail time.Time
	cause     string

	open      bool
	startedAt time.Time
	incident  int64
}

func next(s state, ok bool, cause string, at time.Time, threshold int) (state, event) {
	if ok {
		s.fails, s.firstFail, s.cause = 0, time.Time{}, ""
		if s.open {
			s.open = false
			return s, up
		}
		return s, none
	}

	if s.fails == 0 {
		s.firstFail = at
	}
	s.fails++
	s.cause = cause
	if !s.open && s.fails >= threshold {
		s.open = true
		s.startedAt = s.firstFail
		return s, down
	}
	return s, none
}
