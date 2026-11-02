package dispatcher

import (
	"context"
	"regexp"
	"sort"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/stats"
	"github.com/xtls/xray-core/app/stats/command"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/policy"
	feature_stats "github.com/xtls/xray-core/features/stats"
	"github.com/xtls/xray-core/transport"
)

type testPolicy struct{ stats policy.Stats }

func (testPolicy) Type() interface{}        { return policy.ManagerType() }
func (testPolicy) Start() error             { return nil }
func (testPolicy) Close() error             { return nil }
func (testPolicy) ForSystem() policy.System { return policy.System{} }
func (p testPolicy) ForLevel(uint32) policy.Session {
	s := policy.SessionDefault()
	s.Stats = p.stats
	return s
}

var allUserStats = testPolicy{policy.Stats{UserUplink: true, UserDownlink: true, UserOnline: true}}

func inboundContext(email, ip string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	return session.ContextWithInbound(ctx, &session.Inbound{
		Source: net.TCPDestination(net.ParseAddress(ip), 40000),
		User:   &protocol.MemoryUser{Email: email},
	}), cancel
}

func payload(n int) buf.MultiBuffer {
	return buf.MergeBytes(nil, make([]byte, n))
}

// bytesReader yields the given payloads, one per read.
type bytesReader struct{ sizes []int }

func (r *bytesReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	if len(r.sizes) == 0 {
		return nil, nil
	}
	n := r.sizes[0]
	r.sizes = r.sizes[1:]
	return payload(n), nil
}

func newManager(t *testing.T) *stats.Manager {
	t.Helper()
	m, err := stats.NewManager(context.Background(), &stats.Config{})
	common.Must(err)
	return m
}

func ipEntries(t *testing.T, m *stats.Manager, email string) (online, offline map[string]*command.OnlineIPEntry) {
	t.Helper()
	resp, err := command.NewStatsServer(m).GetUsersStats(context.Background(),
		&command.GetUsersStatsRequest{IncludeOfflineIps: true})
	common.Must(err)
	online, offline = map[string]*command.OnlineIPEntry{}, map[string]*command.OnlineIPEntry{}
	for _, u := range resp.Users {
		if u.Email != email {
			continue
		}
		for _, e := range u.Ips {
			online[e.Ip] = e
		}
		for _, e := range u.OfflineIps {
			offline[e.Ip] = e
		}
	}
	return online, offline
}

func counterValue(m *stats.Manager, name string) int64 {
	if c := m.GetCounter(name); c != nil {
		return c.Value()
	}
	return -1
}

// waitOffline waits for the context.AfterFunc that releases the IP.
func waitOffline(t *testing.T, m *stats.Manager, email, ip string) *command.OnlineIPEntry {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, off := ipEntries(t, m, email); off[ip] != nil {
			return off[ip]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s never went offline", ip)
	return nil
}

// 3x-ui's clientTrafficRegex: anything of this shape is summed into account traffic.
var accountTrafficShape = regexp.MustCompile(`user>>>([^>]+)>>>traffic>>>(downlink|uplink)`)

func accountShapedCounters(m *stats.Manager) []string {
	var names []string
	m.VisitCounters(func(name string, _ feature_stats.Counter) bool {
		if accountTrafficShape.MatchString(name) {
			names = append(names, name)
		}
		return true
	})
	sort.Strings(names)
	return names
}

func TestWrapLinkCountsBytesPerSourceIP(t *testing.T) {
	m := newManager(t)
	ctx, cancel := inboundContext("a@example.com", "203.0.113.7")
	link := WrapLink(ctx, allUserStats, m, &transport.Link{Reader: &bytesReader{sizes: []int{300, 20}}, Writer: buf.Discard})

	for range 2 {
		mb, err := link.Reader.ReadMultiBuffer()
		common.Must(err)
		buf.ReleaseMulti(mb)
	}
	common.Must(link.Writer.WriteMultiBuffer(payload(5000)))

	online, _ := ipEntries(t, m, "a@example.com")
	if e := online["203.0.113.7"]; e == nil || e.GetUplink() != 320 || e.GetDownlink() != 5000 {
		t.Fatalf("online entry = %v, want uplink 320 downlink 5000", e)
	}
	if up, down := counterValue(m, "user>>>a@example.com>>>traffic>>>uplink"), counterValue(m, "user>>>a@example.com>>>traffic>>>downlink"); up != 320 || down != 5000 {
		t.Fatalf("account counters = %d/%d, want 320/5000 (unchanged by per-IP accounting)", up, down)
	}

	cancel() // the connection closes before the next poll
	if e := waitOffline(t, m, "a@example.com", "203.0.113.7"); e.GetUplink() != 320 || e.GetDownlink() != 5000 {
		t.Fatalf("offline entry = %v, want bytes kept after the connection closed", e)
	}
}

func TestGetLinkCountsBytesPerSourceIP(t *testing.T) {
	m := newManager(t)
	d := &DefaultDispatcher{policy: allUserStats, stats: m}
	ctx, cancel := inboundContext("a@example.com", "203.0.113.7")
	inbound, outbound := d.getLink(ctx)

	common.Must(inbound.Writer.WriteMultiBuffer(payload(700)))   // client -> server
	common.Must(outbound.Writer.WriteMultiBuffer(payload(9000))) // server -> client
	for _, r := range []buf.Reader{outbound.Reader, inbound.Reader} {
		mb, err := r.ReadMultiBuffer()
		common.Must(err)
		buf.ReleaseMulti(mb)
	}

	online, _ := ipEntries(t, m, "a@example.com")
	if e := online["203.0.113.7"]; e == nil || e.GetUplink() != 700 || e.GetDownlink() != 9000 {
		t.Fatalf("online entry = %v, want uplink 700 downlink 9000", e)
	}
	if up, down := counterValue(m, "user>>>a@example.com>>>traffic>>>uplink"), counterValue(m, "user>>>a@example.com>>>traffic>>>downlink"); up != 700 || down != 9000 {
		t.Fatalf("account counters = %d/%d, want 700/9000", up, down)
	}

	cancel()
	if e := waitOffline(t, m, "a@example.com", "203.0.113.7"); e.GetUplink() != 700 || e.GetDownlink() != 9000 {
		t.Fatalf("offline entry = %v, want bytes kept after the connection closed", e)
	}
}

// Two IPs of one account: the account counters are the plain sum, and per-IP
// accounting registers no counter that 3x-ui would add to account traffic.
func TestPerIPAccountingDoesNotDoubleCountAccountTraffic(t *testing.T) {
	m := newManager(t)
	for _, ip := range []string{"203.0.113.7", "198.51.100.9"} {
		ctx, cancel := inboundContext("a@example.com", ip)
		defer cancel()
		link := WrapLink(ctx, allUserStats, m, &transport.Link{Reader: &bytesReader{sizes: []int{10}}, Writer: buf.Discard})
		mb, _ := link.Reader.ReadMultiBuffer()
		buf.ReleaseMulti(mb)
		common.Must(link.Writer.WriteMultiBuffer(payload(1000)))
	}

	want := []string{"user>>>a@example.com>>>traffic>>>downlink", "user>>>a@example.com>>>traffic>>>uplink"}
	if got := accountShapedCounters(m); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("account-shaped counters = %v, want exactly %v", got, want)
	}
	if up, down := counterValue(m, want[1]), counterValue(m, want[0]); up != 20 || down != 2000 {
		t.Fatalf("account counters = %d/%d, want 20/2000", up, down)
	}
}

func TestNoPerIPAccountingWithoutStatsUserOnline(t *testing.T) {
	m := newManager(t)
	p := testPolicy{policy.Stats{UserUplink: true, UserDownlink: true}}
	ctx, cancel := inboundContext("a@example.com", "203.0.113.7")
	defer cancel()
	link := WrapLink(ctx, p, m, &transport.Link{Reader: &bytesReader{}, Writer: buf.Discard})
	common.Must(link.Writer.WriteMultiBuffer(payload(1000)))

	if online, offline := ipEntries(t, m, "a@example.com"); len(online)+len(offline) != 0 {
		t.Fatal("source IP tracked although statsUserOnline is off")
	}
	if down := counterValue(m, "user>>>a@example.com>>>traffic>>>downlink"); down != 1000 {
		t.Fatalf("account downlink = %d, want 1000", down)
	}
}

// statsUserOnline alone (no account counters) still accounts bytes per IP.
func TestPerIPAccountingWithOnlyStatsUserOnline(t *testing.T) {
	m := newManager(t)
	p := testPolicy{policy.Stats{UserOnline: true}}
	ctx, cancel := inboundContext("a@example.com", "203.0.113.7")
	defer cancel()
	link := WrapLink(ctx, p, m, &transport.Link{Reader: &bytesReader{sizes: []int{30}}, Writer: buf.Discard})
	mb, _ := link.Reader.ReadMultiBuffer()
	buf.ReleaseMulti(mb)
	common.Must(link.Writer.WriteMultiBuffer(payload(1000)))

	online, _ := ipEntries(t, m, "a@example.com")
	if e := online["203.0.113.7"]; e == nil || e.GetUplink() != 30 || e.GetDownlink() != 1000 {
		t.Fatalf("online entry = %v, want 30/1000", e)
	}
	if got := accountShapedCounters(m); len(got) != 0 {
		t.Fatalf("account counters registered without userUplink/userDownlink: %v", got)
	}
}
