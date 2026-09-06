package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// errProbeTimeout is returned by icmpEngine.probe when no reply arrives
// within the caller's timeout; probers treat any non-nil error as a lost
// probe, so its message only matters for logs.
var errProbeTimeout = errors.New("probe timed out")

// pinger is the subset of icmpEngine that probers depend on, so tests can
// substitute a stub that fabricates RTTs/losses without opening a real raw
// socket (which needs CAP_NET_RAW/root and isn't available in a normal CI
// job).
type pinger interface {
	probe(ctx context.Context, ip net.IP, size int, timeout time.Duration) (time.Duration, error)
}

// pendingKey identifies one in-flight probe. Probers send serially (probe,
// then wait, then sleep until the next tick — see prober.run), so at most
// one probe per (family, ip) is ever outstanding; keying on just that pair
// (no sequence number) is therefore enough to route a reply back to the
// probe that's waiting for it.
type pendingKey struct {
	family int // 4 or 6
	ip     string
}

// icmpEngine sends and receives raw ICMP echo requests for every target,
// sharing one socket per IP family (rather than one per target) to keep the
// process's resource footprint small. Mirrors the pattern from
// golang.org/x/net/icmp's own package example.
type icmpEngine struct {
	id uint16 // process-derived identifier placed in every echo we send

	v4 *icmp.PacketConn
	v6 *icmp.PacketConn

	seq uint32 // shared, monotonically incrementing sequence source

	mu      sync.Mutex
	pending map[pendingKey]chan time.Time

	closed atomic.Bool
}

// newICMPEngine opens the raw ICMPv4 socket (required) and, if enableIPv6,
// the raw ICMPv6 socket. Both require CAP_NET_RAW or root — see the
// Dockerfile and PING_EXPORTER_REQUIREMENTS.md section 6. A missing IPv6
// socket is not fatal: IPv6 targets simply report as lost, logged once here
// rather than crashing a deployment whose host/container lacks IPv6.
func newICMPEngine(enableIPv6 bool) (*icmpEngine, error) {
	e := &icmpEngine{
		id:      uint16(os.Getpid()) & 0xffff,
		pending: make(map[pendingKey]chan time.Time),
	}

	v4conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return nil, fmt.Errorf("opening ICMPv4 raw socket (need CAP_NET_RAW or root): %w", err)
	}
	e.v4 = v4conn
	go e.readLoop(v4conn, 4)

	if enableIPv6 {
		v6conn, err := icmp.ListenPacket("ip6:ipv6-icmp", "::")
		if err != nil {
			log.Printf("ICMPv6 raw socket unavailable, IPv6 targets will report as lost: %v", err)
		} else {
			e.v6 = v6conn
			go e.readLoop(v6conn, 6)
		}
	}

	return e, nil
}

func (e *icmpEngine) Close() error {
	e.closed.Store(true)
	var err error
	if e.v4 != nil {
		err = errors.Join(err, e.v4.Close())
	}
	if e.v6 != nil {
		err = errors.Join(err, e.v6.Close())
	}
	return err
}

// probe sends one echo request to ip and waits up to timeout for the
// matching reply, returning its round-trip time.
func (e *icmpEngine) probe(ctx context.Context, ip net.IP, size int, timeout time.Duration) (time.Duration, error) {
	family := 4
	conn := e.v4
	typ := icmp.Type(ipv4.ICMPTypeEcho)
	if ip.To4() == nil {
		family = 6
		conn = e.v6
		typ = icmp.Type(ipv6.ICMPTypeEchoRequest)
	}
	if conn == nil {
		return 0, fmt.Errorf("no ICMPv%d socket available", family)
	}

	key := pendingKey{family: family, ip: ip.String()}
	replyCh := make(chan time.Time, 1)

	e.mu.Lock()
	e.pending[key] = replyCh
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.pending, key)
		e.mu.Unlock()
	}()

	seq := int(atomic.AddUint32(&e.seq, 1) & 0xffff)
	msg := icmp.Message{
		Type: typ,
		Code: 0,
		Body: &icmp.Echo{
			ID:   int(e.id),
			Seq:  seq,
			Data: makePayload(size),
		},
	}
	wb, err := msg.Marshal(nil)
	if err != nil {
		return 0, fmt.Errorf("marshaling echo request: %w", err)
	}

	start := time.Now()
	if _, err := conn.WriteTo(wb, &net.IPAddr{IP: ip}); err != nil {
		return 0, fmt.Errorf("writing echo request: %w", err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case t := <-replyCh:
		return t.Sub(start), nil
	case <-timer.C:
		return 0, errProbeTimeout
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// readLoop dispatches every echo reply arriving on conn to whichever probe
// is waiting for that (family, source IP), if any; a reply with no waiter
// (already timed out, or a stray unrelated ICMP packet) is silently
// dropped.
func (e *icmpEngine) readLoop(conn *icmp.PacketConn, family int) {
	protoNum := ipv4.ICMPTypeEchoReply.Protocol()
	if family == 6 {
		protoNum = ipv6.ICMPTypeEchoReply.Protocol()
	}

	buf := make([]byte, 1500)
	for {
		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			if e.closed.Load() {
				return
			}
			continue
		}

		rm, err := icmp.ParseMessage(protoNum, buf[:n])
		if err != nil {
			continue
		}

		switch family {
		case 4:
			if rm.Type != ipv4.ICMPTypeEchoReply {
				continue
			}
		case 6:
			if rm.Type != ipv6.ICMPTypeEchoReply {
				continue
			}
		}

		echo, ok := rm.Body.(*icmp.Echo)
		if !ok || echo.ID != int(e.id) {
			continue
		}

		now := time.Now()
		key := pendingKey{family: family, ip: peer.String()}

		e.mu.Lock()
		ch, exists := e.pending[key]
		e.mu.Unlock()

		if exists {
			select {
			case ch <- now:
			default:
			}
		}
	}
}

func makePayload(size int) []byte {
	if size < 0 {
		size = 0
	}
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}
