package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

// prober continuously pings one resolved (target, ip) pair on its own
// goroutine and keeps a rolling history of the results.
type prober struct {
	target    string
	ip        net.IP
	ipVersion string
	history   *pingHistory
	cancel    context.CancelFunc
	done      chan struct{} // closed when run() returns
}

// run pings on cfg.Ping.Interval until ctx is canceled, updating this
// target/IP's metrics after every attempt. The first probe fires
// immediately (not after waiting a full interval) so a fresh series shows
// up in /metrics within a couple of seconds of startup, per
// PING_EXPORTER_REQUIREMENTS.md section 6.
//
// Probes are sent serially — the next tick is only acted on once the
// current probe has returned or timed out — so a lossy target's effective
// cadence stretches toward ping.timeout instead of overlapping requests.
// With this deployment's config (timeout 3s > interval 2s) that trade-off
// only shows up while a target is actually dropping packets.
func (p *prober) run(ctx context.Context, eng pinger, interval, timeout time.Duration, size int) {
	defer close(p.done)

	p.probeOnce(ctx, eng, timeout, size)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.probeOnce(ctx, eng, timeout, size)
		}
	}
}

func (p *prober) probeOnce(ctx context.Context, eng pinger, timeout time.Duration, size int) {
	rtt, err := eng.probe(ctx, p.ip, size, timeout)
	p.history.record(err == nil, rtt)
	applyStats(p.target, p.ip.String(), p.ipVersion, p.history.stats())
}

// proberKey identifies one running prober by the target string as
// configured and the specific resolved IP it probes — a hostname target
// that resolves to multiple IPs gets one prober per IP.
type proberKey struct {
	target string
	ip     string
}

// manager owns every prober, resolves targets (once at startup, and
// periodically for hostnames per dns.refresh), and reconciles the running
// prober set when a hostname's resolved addresses change.
type manager struct {
	cfg Config
	eng pinger

	// lookup is resolver.LookupIPAddr by default; overridden in tests to
	// avoid depending on real DNS.
	lookup func(ctx context.Context, host string) ([]net.IPAddr, error)

	mu      sync.Mutex
	probers map[proberKey]*prober

	// refreshCancel stops every refreshLoop goroutine started by Start;
	// refreshWG lets stopAll block until they've actually exited, the same
	// way stopProber blocks on a prober's done channel. Defaults to a no-op
	// so stopAll is safe to call on a manager whose Start was never called.
	refreshCancel context.CancelFunc
	refreshWG     sync.WaitGroup
}

func newManager(cfg Config, eng pinger) *manager {
	resolver := newDNSResolver(cfg.DNS.Nameserver)
	return &manager{
		cfg:           cfg,
		eng:           eng,
		lookup:        resolver.LookupIPAddr,
		probers:       make(map[proberKey]*prober),
		refreshCancel: func() {},
	}
}

// newDNSResolver builds a resolver that always queries dns.nameserver
// directly (when set), overriding the system resolver — matching upstream
// ping_exporter's dns.nameserver option.
func newDNSResolver(nameserver string) *net.Resolver {
	if nameserver == "" {
		return net.DefaultResolver
	}
	addr := net.JoinHostPort(nameserver, "53")
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			return d.DialContext(ctx, network, addr)
		},
	}
}

// Start resolves every configured target once and launches a prober for
// each resolved IP. It returns an error (meant to be fatal — see main.go)
// if any target can't be resolved at all on this first attempt, per
// PING_EXPORTER_REQUIREMENTS.md section 6 ("unresolvable target at
// startup" should fail fast rather than run with gaps silently). Hostname
// targets additionally get a background goroutine that re-resolves every
// dns.refresh and reconciles the prober set.
func (m *manager) Start(ctx context.Context) error {
	refreshCtx, cancel := context.WithCancel(ctx)
	m.refreshCancel = cancel

	for _, target := range m.cfg.Targets {
		resolveCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		ips, err := m.resolveTarget(resolveCtx, target)
		cancel()
		if err != nil {
			return fmt.Errorf("resolving target %q: %w", target, err)
		}

		for _, ip := range ips {
			m.startProber(target, ip)
		}

		if net.ParseIP(target) == nil {
			m.refreshWG.Add(1)
			go m.refreshLoop(refreshCtx, target)
		}
	}
	return nil
}

func (m *manager) v6Enabled() bool { return !m.cfg.Options.DisableIPv6 }

// resolveTarget resolves one configured target string to the IP(s) that
// should be probed: itself, if it's already an IP literal, or the result of
// a DNS lookup (filtered to IPv4-only when options.disableIPv6 is set).
func (m *manager) resolveTarget(ctx context.Context, target string) ([]net.IP, error) {
	if ip := net.ParseIP(target); ip != nil {
		if ip.To4() == nil && !m.v6Enabled() {
			return nil, fmt.Errorf("target is an IPv6 literal but disableIPv6 is true")
		}
		return []net.IP{ip}, nil
	}

	addrs, err := m.lookup(ctx, target)
	if err != nil {
		return nil, err
	}

	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		if a.IP.To4() == nil && !m.v6Enabled() {
			continue
		}
		ips = append(ips, a.IP)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no usable addresses (disableIPv6=%v)", !m.v6Enabled())
	}
	return ips, nil
}

func (m *manager) startProber(target string, ip net.IP) {
	key := proberKey{target: target, ip: ip.String()}

	m.mu.Lock()
	if _, exists := m.probers[key]; exists {
		m.mu.Unlock()
		return
	}
	ipVersion := "4"
	if ip.To4() == nil {
		ipVersion = "6"
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &prober{
		target:    target,
		ip:        ip,
		ipVersion: ipVersion,
		history:   newPingHistory(m.cfg.Ping.HistorySize),
		cancel:    cancel,
		done:      make(chan struct{}),
	}
	m.probers[key] = p
	m.mu.Unlock()

	go p.run(ctx, m.eng, m.cfg.Ping.Interval.Duration(), m.cfg.Ping.Timeout.Duration(), m.cfg.Ping.Size)
}

// stopProber cancels the prober's goroutine, waits for it to actually exit,
// and only then deletes its metric series. Waiting matters: without it, a
// probe already in flight when cancel() is called could still be partway
// through applyStats (which re-creates a series via WithLabelValues) and
// finish *after* removeSeries runs, leaving a stale series behind forever.
func (m *manager) stopProber(key proberKey) {
	m.mu.Lock()
	p, exists := m.probers[key]
	if exists {
		delete(m.probers, key)
	}
	m.mu.Unlock()

	if exists {
		p.cancel()
		<-p.done
		removeSeries(p.target, p.ip.String(), p.ipVersion)
	}
}

// stopAll cancels every running refreshLoop and prober and clears the
// probers' metrics series. The running process never needs this itself
// (exiting reclaims everything), but tests use it to leave no goroutines
// behind between cases.
//
// refreshLoop is stopped (and waited on) first: otherwise a refresh tick
// racing this call could reconcile a stopped target back in, starting a
// fresh prober after stopAll has already collected the set to stop.
func (m *manager) stopAll() {
	m.refreshCancel()
	m.refreshWG.Wait()

	m.mu.Lock()
	keys := make([]proberKey, 0, len(m.probers))
	for k := range m.probers {
		keys = append(keys, k)
	}
	m.mu.Unlock()

	for _, k := range keys {
		m.stopProber(k)
	}
}

func (m *manager) refreshLoop(ctx context.Context, target string) {
	defer m.refreshWG.Done()

	interval := m.cfg.DNS.Refresh.Duration()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			resolveCtx, cancel := context.WithTimeout(ctx, interval)
			ips, err := m.resolveTarget(resolveCtx, target)
			cancel()
			if err != nil {
				log.Printf("re-resolving %q: %v (keeping previous addresses)", target, err)
				continue
			}
			m.reconcile(target, ips)
		}
	}
}

// reconcile brings the running prober set for target in line with desired:
// it starts a prober for any newly-resolved IP and stops (and removes the
// metrics series for) any prober whose IP is no longer present, so a
// dynamic-DNS change is reflected without a restart.
func (m *manager) reconcile(target string, desired []net.IP) {
	desiredSet := make(map[string]net.IP, len(desired))
	for _, ip := range desired {
		desiredSet[ip.String()] = ip
	}

	m.mu.Lock()
	var toStop []proberKey
	for key := range m.probers {
		if key.target != target {
			continue
		}
		if _, ok := desiredSet[key.ip]; !ok {
			toStop = append(toStop, key)
		}
	}
	var toStart []net.IP
	for ipStr, ip := range desiredSet {
		if _, exists := m.probers[proberKey{target: target, ip: ipStr}]; !exists {
			toStart = append(toStart, ip)
		}
	}
	m.mu.Unlock()

	for _, key := range toStop {
		m.stopProber(key)
	}
	for _, ip := range toStart {
		m.startProber(target, ip)
	}
}
