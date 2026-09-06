package main

import (
	"math"
	"sync"
	"time"
)

// sample is one probe outcome: either a successful echo with its RTT, or a
// loss (ok == false).
type sample struct {
	ok  bool
	rtt time.Duration
}

// pingHistory is a fixed-size ring buffer of the most recent probe outcomes
// for one target/IP, matching upstream ping_exporter's windowed-stats
// semantics: ping.history-size caps how many recent probes feed the rolling
// loss/RTT metrics, so a target's numbers reflect recent behavior rather
// than an ever-growing all-time average.
type pingHistory struct {
	mu      sync.Mutex
	samples []sample
	next    int // next write index
	filled  int // valid entries so far, caps at len(samples)
}

func newPingHistory(size int) *pingHistory {
	if size < 1 {
		size = 1
	}
	return &pingHistory{samples: make([]sample, size)}
}

func (h *pingHistory) record(ok bool, rtt time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.samples[h.next] = sample{ok: ok, rtt: rtt}
	h.next = (h.next + 1) % len(h.samples)
	if h.filled < len(h.samples) {
		h.filled++
	}
}

// stats summarizes the current window. RTT fields are only meaningful when
// haveRTT is true (the window contains at least one successful probe) —
// loss ratio, by contrast, is always meaningful once the window is
// non-empty, since it counts every attempt (success + loss).
type stats struct {
	haveData  bool
	lossRatio float64
	haveRTT   bool
	best      time.Duration
	worst     time.Duration
	mean      time.Duration
	stddev    time.Duration
}

// stats computes best/worst/mean/stddev over successful samples only, and
// loss ratio over every sample in the window — the same split upstream
// ping_exporter uses: a target that's fully down still needs a loss ratio
// of 1.0, but has no RTT numbers to report.
func (h *pingHistory) stats() stats {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.filled == 0 {
		return stats{}
	}

	var lost int
	rtts := make([]time.Duration, 0, h.filled)
	for i := 0; i < h.filled; i++ {
		s := h.samples[i]
		if s.ok {
			rtts = append(rtts, s.rtt)
		} else {
			lost++
		}
	}

	st := stats{
		haveData:  true,
		lossRatio: float64(lost) / float64(h.filled),
	}
	if len(rtts) == 0 {
		return st
	}

	st.haveRTT = true
	st.best, st.worst = rtts[0], rtts[0]
	var sum time.Duration
	for _, r := range rtts {
		if r < st.best {
			st.best = r
		}
		if r > st.worst {
			st.worst = r
		}
		sum += r
	}
	st.mean = sum / time.Duration(len(rtts))

	var sqDiffSum float64
	meanF := float64(st.mean)
	for _, r := range rtts {
		d := float64(r) - meanF
		sqDiffSum += d * d
	}
	st.stddev = time.Duration(math.Sqrt(sqDiffSum / float64(len(rtts))))

	return st
}
