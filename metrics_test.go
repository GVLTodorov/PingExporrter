package main

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestApplyStatsWithRTT(t *testing.T) {
	lossRatio.Reset()
	rttBest.Reset()
	rttMean.Reset()
	rttStdDeviation.Reset()
	rttWorst.Reset()

	applyStats("example.com", "1.2.3.4", "4", stats{
		haveData:  true,
		lossRatio: 0.25,
		haveRTT:   true,
		best:      10 * time.Millisecond,
		mean:      20 * time.Millisecond,
		stddev:    5 * time.Millisecond,
		worst:     30 * time.Millisecond,
	})

	if got := testutil.ToFloat64(lossRatio.WithLabelValues("1.2.3.4", "4", "example.com")); got != 0.25 {
		t.Errorf("lossRatio = %v, want 0.25", got)
	}
	if got := testutil.ToFloat64(rttBest.WithLabelValues("1.2.3.4", "4", "example.com")); got != 0.01 {
		t.Errorf("rttBest = %v, want 0.01", got)
	}
	if got := testutil.ToFloat64(rttMean.WithLabelValues("1.2.3.4", "4", "example.com")); got != 0.02 {
		t.Errorf("rttMean = %v, want 0.02", got)
	}
	if got := testutil.ToFloat64(rttStdDeviation.WithLabelValues("1.2.3.4", "4", "example.com")); got != 0.005 {
		t.Errorf("rttStdDeviation = %v, want 0.005", got)
	}
	if got := testutil.ToFloat64(rttWorst.WithLabelValues("1.2.3.4", "4", "example.com")); got != 0.03 {
		t.Errorf("rttWorst = %v, want 0.03", got)
	}
}

// TestApplyStatsNoRTTStillCreatesSeries checks the acceptance-criteria
// requirement that every one of the five per-target metrics has a series
// for every configured target/IP-version pair, even before any probe has
// succeeded (a target that's down from the first probe still needs
// rtt_best_seconds etc. present, just at 0, rather than absent).
func TestApplyStatsNoRTTStillCreatesSeries(t *testing.T) {
	lossRatio.Reset()
	rttBest.Reset()
	rttMean.Reset()
	rttStdDeviation.Reset()
	rttWorst.Reset()

	applyStats("192.168.1.1", "192.168.1.1", "4", stats{haveData: true, lossRatio: 1, haveRTT: false})

	if got := testutil.ToFloat64(lossRatio.WithLabelValues("192.168.1.1", "4", "192.168.1.1")); got != 1 {
		t.Errorf("lossRatio = %v, want 1", got)
	}
	if got := testutil.ToFloat64(rttBest.WithLabelValues("192.168.1.1", "4", "192.168.1.1")); got != 0 {
		t.Errorf("rttBest = %v, want 0", got)
	}
	if n := testutil.CollectAndCount(rttBest); n != 1 {
		t.Errorf("rttBest series count = %d, want 1", n)
	}
}

func TestRemoveSeries(t *testing.T) {
	lossRatio.Reset()
	rttBest.Reset()
	rttMean.Reset()
	rttStdDeviation.Reset()
	rttWorst.Reset()

	applyStats("dyn.example.com", "10.0.0.5", "4", stats{haveData: true, lossRatio: 0, haveRTT: true, best: time.Millisecond, mean: time.Millisecond, worst: time.Millisecond})
	if n := testutil.CollectAndCount(lossRatio); n != 1 {
		t.Fatalf("lossRatio series count = %d, want 1 before removal", n)
	}

	removeSeries("dyn.example.com", "10.0.0.5", "4")

	if n := testutil.CollectAndCount(lossRatio); n != 0 {
		t.Errorf("lossRatio series count = %d, want 0 after removeSeries", n)
	}
	if n := testutil.CollectAndCount(rttBest); n != 0 {
		t.Errorf("rttBest series count = %d, want 0 after removeSeries", n)
	}
}

func TestUpMetricCarriesVersionLabel(t *testing.T) {
	up.Reset()
	up.WithLabelValues("1.2.3").Set(1)

	if got := testutil.ToFloat64(up.WithLabelValues("1.2.3")); got != 1 {
		t.Errorf("up{version=1.2.3} = %v, want 1", got)
	}
}
