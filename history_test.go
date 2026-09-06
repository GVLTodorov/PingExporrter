package main

import (
	"math"
	"testing"
	"time"
)

func TestPingHistoryEmpty(t *testing.T) {
	h := newPingHistory(5)
	st := h.stats()
	if st.haveData {
		t.Error("haveData = true, want false for an empty history")
	}
}

func TestPingHistoryAllSuccess(t *testing.T) {
	h := newPingHistory(5)
	for _, ms := range []int{10, 20, 30, 40, 50} {
		h.record(true, time.Duration(ms)*time.Millisecond)
	}

	st := h.stats()
	if !st.haveRTT {
		t.Fatal("haveRTT = false, want true")
	}
	if st.lossRatio != 0 {
		t.Errorf("lossRatio = %v, want 0", st.lossRatio)
	}
	if st.best != 10*time.Millisecond {
		t.Errorf("best = %v, want 10ms", st.best)
	}
	if st.worst != 50*time.Millisecond {
		t.Errorf("worst = %v, want 50ms", st.worst)
	}
	if st.mean != 30*time.Millisecond {
		t.Errorf("mean = %v, want 30ms", st.mean)
	}
	// stddev of [10,20,30,40,50] (population) = sqrt(200) ms ~= 14.142ms
	wantStdDev := time.Duration(math.Sqrt(200) * float64(time.Millisecond))
	if diff := st.stddev - wantStdDev; diff > time.Microsecond || diff < -time.Microsecond {
		t.Errorf("stddev = %v, want ~%v", st.stddev, wantStdDev)
	}
}

func TestPingHistoryAllLoss(t *testing.T) {
	h := newPingHistory(3)
	h.record(false, 0)
	h.record(false, 0)
	h.record(false, 0)

	st := h.stats()
	if !st.haveData {
		t.Fatal("haveData = false, want true")
	}
	if st.haveRTT {
		t.Error("haveRTT = true, want false when every sample was lost")
	}
	if st.lossRatio != 1 {
		t.Errorf("lossRatio = %v, want 1", st.lossRatio)
	}
}

func TestPingHistoryMixed(t *testing.T) {
	h := newPingHistory(4)
	h.record(true, 10*time.Millisecond)
	h.record(false, 0)
	h.record(true, 20*time.Millisecond)
	h.record(false, 0)

	st := h.stats()
	if st.lossRatio != 0.5 {
		t.Errorf("lossRatio = %v, want 0.5", st.lossRatio)
	}
	if !st.haveRTT {
		t.Fatal("haveRTT = false, want true")
	}
	if st.mean != 15*time.Millisecond {
		t.Errorf("mean = %v, want 15ms (computed only over the two successes)", st.mean)
	}
}

// TestPingHistoryWindowSlides verifies the ring buffer drops the oldest
// sample once it's full, so loss_ratio/RTT reflect only the most recent
// history-size probes rather than an ever-growing all-time average.
func TestPingHistoryWindowSlides(t *testing.T) {
	h := newPingHistory(3)
	h.record(false, 0)
	h.record(false, 0)
	h.record(false, 0)
	if got := h.stats().lossRatio; got != 1 {
		t.Fatalf("lossRatio = %v, want 1 before the window slides", got)
	}

	// Three more successes push all three losses out of the window.
	h.record(true, 5*time.Millisecond)
	h.record(true, 5*time.Millisecond)
	h.record(true, 5*time.Millisecond)

	st := h.stats()
	if st.lossRatio != 0 {
		t.Errorf("lossRatio = %v, want 0 after the window slides past every loss", st.lossRatio)
	}
}

func TestNewPingHistoryClampsSizeToOne(t *testing.T) {
	h := newPingHistory(0)
	if len(h.samples) != 1 {
		t.Fatalf("len(samples) = %d, want 1 for a non-positive size", len(h.samples))
	}
}
