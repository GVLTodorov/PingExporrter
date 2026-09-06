package main

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.uber.org/goleak"
)

// TestMain runs a whole-package goroutine-leak check after every test
// finishes. manager spawns a long-lived goroutine per prober, so this
// guards against a test (or the production stopProber/stopAll paths it
// exercises) leaving one running past the test that started it.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// stubPinger is a pinger that never touches the network, so manager/prober
// tests can run instantly and without CAP_NET_RAW.
type stubPinger struct {
	mu    sync.Mutex
	rtt   time.Duration
	err   error
	calls int
}

func (s *stubPinger) probe(ctx context.Context, ip net.IP, size int, timeout time.Duration) (time.Duration, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.rtt, s.err
}

func testConfig() Config {
	return Config{
		Ping: PingConfig{
			Interval:    Duration(time.Hour), // long enough that no test ever sees a second tick
			Timeout:     Duration(time.Second),
			HistorySize: 5,
			Size:        56,
		},
		DNS: DNSConfig{Refresh: Duration(time.Hour)},
	}
}

func TestResolveTargetIPv4Literal(t *testing.T) {
	m := newManager(testConfig(), &stubPinger{})
	ips, err := m.resolveTarget(context.Background(), "8.8.8.8")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ips) != 1 || ips[0].String() != "8.8.8.8" {
		t.Errorf("ips = %v, want [8.8.8.8]", ips)
	}
}

func TestResolveTargetIPv6LiteralRejectedWhenDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.Options.DisableIPv6 = true
	m := newManager(cfg, &stubPinger{})

	if _, err := m.resolveTarget(context.Background(), "2001:4860:4860::8888"); err == nil {
		t.Fatal("expected an error resolving an IPv6 literal target with disableIPv6=true")
	}
}

func TestResolveTargetHostname(t *testing.T) {
	m := newManager(testConfig(), &stubPinger{})
	m.lookup = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		if host != "example.com" {
			t.Fatalf("lookup called with host = %q, want example.com", host)
		}
		return []net.IPAddr{
			{IP: net.ParseIP("93.184.216.34")},
			{IP: net.ParseIP("2606:2800:220:1:248:1893:25c8:1946")},
		}, nil
	}

	ips, err := m.resolveTarget(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ips) != 2 {
		t.Fatalf("len(ips) = %d, want 2", len(ips))
	}
}

func TestResolveTargetHostnameFiltersIPv6WhenDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.Options.DisableIPv6 = true
	m := newManager(cfg, &stubPinger{})
	m.lookup = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{
			{IP: net.ParseIP("93.184.216.34")},
			{IP: net.ParseIP("2606:2800:220:1:248:1893:25c8:1946")},
		}, nil
	}

	ips, err := m.resolveTarget(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ips) != 1 || ips[0].To4() == nil {
		t.Errorf("ips = %v, want exactly one IPv4 address", ips)
	}
}

func TestResolveTargetHostnameLookupError(t *testing.T) {
	m := newManager(testConfig(), &stubPinger{})
	m.lookup = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return nil, errors.New("no such host")
	}
	if _, err := m.resolveTarget(context.Background(), "does-not-resolve.invalid"); err == nil {
		t.Fatal("expected an error when the lookup fails")
	}
}

func TestResolveTargetHostnameOnlyIPv6ButDisabled(t *testing.T) {
	cfg := testConfig()
	cfg.Options.DisableIPv6 = true
	m := newManager(cfg, &stubPinger{})
	m.lookup = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("2606:2800:220:1:248:1893:25c8:1946")}}, nil
	}
	if _, err := m.resolveTarget(context.Background(), "v6-only.invalid"); err == nil {
		t.Fatal("expected an error when every resolved address is filtered out")
	}
}

func TestStartLaunchesOneProberPerTarget(t *testing.T) {
	m := newManager(testConfig(), &stubPinger{})
	m.cfg.Targets = []string{"1.1.1.1", "1.0.0.1"}
	t.Cleanup(m.stopAll)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m.mu.Lock()
	n := len(m.probers)
	m.mu.Unlock()
	if n != 2 {
		t.Errorf("len(probers) = %d, want 2", n)
	}
}

func TestStartFailsFastOnUnresolvableTarget(t *testing.T) {
	m := newManager(testConfig(), &stubPinger{})
	m.cfg.Targets = []string{"1.1.1.1", "unresolvable.invalid"}
	m.lookup = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return nil, errors.New("no such host")
	}
	t.Cleanup(m.stopAll)

	if err := m.Start(context.Background()); err == nil {
		t.Fatal("expected Start to return an error when a target can't be resolved")
	}
}

func TestStartProberIsIdempotentPerKey(t *testing.T) {
	m := newManager(testConfig(), &stubPinger{})
	t.Cleanup(m.stopAll)

	ip := net.ParseIP("192.168.1.1")
	m.startProber("192.168.1.1", ip)
	m.startProber("192.168.1.1", ip)

	m.mu.Lock()
	n := len(m.probers)
	m.mu.Unlock()
	if n != 1 {
		t.Errorf("len(probers) = %d, want 1 (duplicate start should be a no-op)", n)
	}
}

func TestStartProberSetsIPVersionLabel(t *testing.T) {
	m := newManager(testConfig(), &stubPinger{})
	t.Cleanup(m.stopAll)

	m.startProber("host", net.ParseIP("192.168.1.1"))
	m.startProber("host6", net.ParseIP("2001:4860:4860::8888"))

	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.probers[proberKey{target: "host", ip: "192.168.1.1"}]; p == nil || p.ipVersion != "4" {
		t.Errorf("ipVersion for the v4 target = %+v, want 4", p)
	}
	if p := m.probers[proberKey{target: "host6", ip: "2001:4860:4860::8888"}]; p == nil || p.ipVersion != "6" {
		t.Errorf("ipVersion for the v6 target = %+v, want 6", p)
	}
}

func TestStopProberRemovesMetrics(t *testing.T) {
	lossRatio.Reset()

	m := newManager(testConfig(), &stubPinger{})
	ip := net.ParseIP("10.0.0.1")
	m.startProber("target", ip)

	// The prober's first probe runs on its own goroutine, so give it a
	// moment to land before checking the series exists.
	wantLabels := map[string]string{"ip": "10.0.0.1", "ip_version": "4", "target": "target"}
	waitForCondition(t, func() bool { return metricSeriesExists(lossRatio, wantLabels) })

	m.stopProber(proberKey{target: "target", ip: "10.0.0.1"})

	if metricSeriesExists(lossRatio, wantLabels) {
		t.Error("lossRatio series still present after stopProber")
	}
}

func TestReconcileStartsAndStopsProbers(t *testing.T) {
	lossRatio.Reset()

	m := newManager(testConfig(), &stubPinger{})
	t.Cleanup(m.stopAll)

	oldIP := net.ParseIP("10.0.0.1")
	keptIP := net.ParseIP("10.0.0.2")
	m.startProber("dyn.example.com", oldIP)
	m.startProber("dyn.example.com", keptIP)

	newIP := net.ParseIP("10.0.0.3")
	m.reconcile("dyn.example.com", []net.IP{keptIP, newIP})

	m.mu.Lock()
	_, hasOld := m.probers[proberKey{target: "dyn.example.com", ip: "10.0.0.1"}]
	_, hasKept := m.probers[proberKey{target: "dyn.example.com", ip: "10.0.0.2"}]
	_, hasNew := m.probers[proberKey{target: "dyn.example.com", ip: "10.0.0.3"}]
	n := len(m.probers)
	m.mu.Unlock()

	if hasOld {
		t.Error("stale IP 10.0.0.1 still has a running prober after reconcile")
	}
	if !hasKept {
		t.Error("10.0.0.2 (still desired) lost its prober after reconcile")
	}
	if !hasNew {
		t.Error("newly-desired IP 10.0.0.3 has no prober after reconcile")
	}
	if n != 2 {
		t.Errorf("len(probers) = %d, want 2", n)
	}
}

// TestStopAllStopsHostnameRefreshLoop guards against a real leak: Start
// spawns one refreshLoop goroutine per hostname target with no way to stop
// it, so it (and, worse, any prober it resurrects via reconcile) outlived
// stopAll and every other test in the package. The whole-package goleak
// check in TestMain would only ever surface that at the very end of the
// test binary, blamed on whatever test happened to run last — so this test
// also does its own scoped check via goleak.Find, with retry, so a
// regression is reported here and can't race a concurrently-running test.
func TestStopAllStopsHostnameRefreshLoop(t *testing.T) {
	cfg := testConfig()
	cfg.Targets = []string{"dyn.example.com"}
	cfg.DNS.Refresh = Duration(time.Millisecond) // fire immediately so the loop is definitely running
	m := newManager(cfg, &stubPinger{})
	m.lookup = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("10.0.0.1")}}, nil
	}

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	time.Sleep(20 * time.Millisecond) // let refreshLoop tick a few times

	m.stopAll()

	if err := goleak.Find(); err != nil {
		t.Errorf("goroutine(s) still running after stopAll: %v", err)
	}
}

func TestReconcileDoesNotTouchOtherTargets(t *testing.T) {
	m := newManager(testConfig(), &stubPinger{})
	t.Cleanup(m.stopAll)

	m.startProber("other.example.com", net.ParseIP("10.0.0.9"))
	m.reconcile("dyn.example.com", nil)

	m.mu.Lock()
	_, hasOther := m.probers[proberKey{target: "other.example.com", ip: "10.0.0.9"}]
	m.mu.Unlock()
	if !hasOther {
		t.Error("reconcile for one target removed a prober belonging to a different target")
	}
}

// --- helpers ---

// metricSeriesExists reports whether vec currently has a series whose
// labels exactly match want, without the side effect WithLabelValues would
// have of creating one.
func metricSeriesExists(vec *prometheus.GaugeVec, want map[string]string) bool {
	ch := make(chan prometheus.Metric, 64)
	vec.Collect(ch)
	close(ch)

	for m := range ch {
		var got dto.Metric
		if err := m.Write(&got); err != nil {
			continue
		}
		if len(got.Label) != len(want) {
			continue
		}
		match := true
		for _, l := range got.Label {
			if want[l.GetName()] != l.GetValue() {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func waitForCondition(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("condition not met before deadline")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
