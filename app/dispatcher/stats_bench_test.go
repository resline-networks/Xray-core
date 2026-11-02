package dispatcher_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/app/stats"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/log"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/policy"
	"github.com/xtls/xray-core/transport"
)

// benchPolicy turns on every per-user stat, as 3x-ui does on the relays.
type benchPolicy struct{}

func (benchPolicy) Type() interface{} { return policy.ManagerType() }
func (benchPolicy) Start() error      { return nil }
func (benchPolicy) Close() error      { return nil }
func (benchPolicy) ForSystem() policy.System {
	return policy.System{}
}
func (benchPolicy) ForLevel(uint32) policy.Session {
	s := policy.SessionDefault()
	s.Stats.UserUplink = true
	s.Stats.UserDownlink = true
	s.Stats.UserOnline = true
	return s
}

func benchContext(email, ip string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ctx = session.ContextWithInbound(ctx, &session.Inbound{
		Source: net.TCPDestination(net.ParseAddress(ip), 40000),
		User:   &protocol.MemoryUser{Email: email},
	})
	return ctx, cancel
}

// endlessReader hands out one freshly filled buffer per read, like a socket.
type endlessReader struct{}

func (endlessReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	b := buf.New()
	b.Extend(buf.Size)
	return buf.MultiBuffer{b}, nil
}

type discardLog struct{}

func (discardLog) Handle(log.Message) {}

func newBenchManager(b *testing.B) *stats.Manager {
	log.RegisterHandler(discardLog{})
	m, err := stats.NewManager(context.Background(), &stats.Config{})
	if err != nil {
		b.Fatal(err)
	}
	return m
}

// BenchmarkWrapLinkTraffic pushes buffers through a wrapped link in both
// directions, one link per goroutine, every link on its own source IP.
func BenchmarkWrapLinkTraffic(b *testing.B) {
	m := newBenchManager(b)
	var seq atomic.Int64
	b.SetBytes(2 * buf.Size)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		n := seq.Add(1)
		ctx, cancel := benchContext("bench@example.com", fmt.Sprintf("203.0.113.%d", n%250+1))
		defer cancel()
		link := dispatcher.WrapLink(ctx, benchPolicy{}, m, &transport.Link{Reader: endlessReader{}, Writer: buf.Discard})
		for pb.Next() {
			mb, err := link.Reader.ReadMultiBuffer()
			if err != nil {
				b.Fatal(err)
			}
			if err := link.Writer.WriteMultiBuffer(mb); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkWrapLinkChurn opens and closes connections from a rotating set of
// source IPs (CGNAT-like), moving one buffer each way per connection.
func BenchmarkWrapLinkChurn(b *testing.B) {
	m := newBenchManager(b)
	var seq atomic.Int64
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := seq.Add(1)
			ip := fmt.Sprintf("198.51.%d.%d", (n/250)%250, n%250+1)
			ctx, cancel := benchContext(fmt.Sprintf("u%d@example.com", n%64), ip)
			link := dispatcher.WrapLink(ctx, benchPolicy{}, m, &transport.Link{Reader: endlessReader{}, Writer: buf.Discard})
			mb, _ := link.Reader.ReadMultiBuffer()
			_ = link.Writer.WriteMultiBuffer(mb)
			cancel()
		}
	})
}

// BenchmarkWrapLinkReconnect opens and closes connections from a fixed set of
// devices (4 source IPs for each of 64 users), the steady state on a relay
// where clients reconnect for every health check.
func BenchmarkWrapLinkReconnect(b *testing.B) {
	m := newBenchManager(b)
	var seq atomic.Int64
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := seq.Add(1)
			user := n % 64
			ip := fmt.Sprintf("198.51.%d.%d", user, (n/64)%4+1)
			ctx, cancel := benchContext(fmt.Sprintf("u%d@example.com", user), ip)
			link := dispatcher.WrapLink(ctx, benchPolicy{}, m, &transport.Link{Reader: endlessReader{}, Writer: buf.Discard})
			mb, _ := link.Reader.ReadMultiBuffer()
			_ = link.Writer.WriteMultiBuffer(mb)
			cancel()
		}
	})
}
