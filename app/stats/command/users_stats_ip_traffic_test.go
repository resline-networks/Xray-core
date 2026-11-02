package command_test

import (
	"context"
	"testing"

	"github.com/xtls/xray-core/app/stats"
	. "github.com/xtls/xray-core/app/stats/command"
	"github.com/xtls/xray-core/common"
	feature_stats "github.com/xtls/xray-core/features/stats"
)

// connect opens one connection from ip for email and moves up/down bytes.
func connect(t *testing.T, m *stats.Manager, email, ip string, up, down int64) {
	t.Helper()
	om, err := m.GetOrRegisterOnlineMap("user>>>" + email + ">>>online")
	common.Must(err)
	u, d := om.(feature_stats.IPTrafficOnlineMap).AddIPWithTraffic(ip)
	u.Add(up)
	d.Add(down)
}

func disconnect(t *testing.T, m *stats.Manager, email, ip string) {
	t.Helper()
	m.GetOnlineMap("user>>>" + email + ">>>online").RemoveIP(ip)
}

func usersByEmail(resp *GetUsersStatsResponse) map[string]*UserStat {
	out := map[string]*UserStat{}
	for _, u := range resp.Users {
		out[u.Email] = u
	}
	return out
}

func entriesByIP(entries []*OnlineIPEntry) map[string]*OnlineIPEntry {
	out := map[string]*OnlineIPEntry{}
	for _, e := range entries {
		out[e.Ip] = e
	}
	return out
}

func TestGetUsersStatsReportsBytesPerOnlineIP(t *testing.T) {
	m, err := stats.NewManager(context.Background(), &stats.Config{})
	common.Must(err)
	connect(t, m, "a@example.com", "203.0.113.7", 100, 2000)
	connect(t, m, "a@example.com", "198.51.100.9", 3, 40)

	resp, err := NewStatsServer(m).GetUsersStats(context.Background(), &GetUsersStatsRequest{})
	common.Must(err)

	ips := entriesByIP(usersByEmail(resp)["a@example.com"].Ips)
	if e := ips["203.0.113.7"]; e == nil || e.GetUplink() != 100 || e.GetDownlink() != 2000 {
		t.Fatalf("203.0.113.7 entry = %v, want 100/2000", e)
	}
	if e := ips["198.51.100.9"]; e == nil || e.GetUplink() != 3 || e.GetDownlink() != 40 {
		t.Fatalf("198.51.100.9 entry = %v, want 3/40", e)
	}
}

// A connection that opens and closes between two polls must still be counted,
// but only for callers that ask for offline IPs: old callers (stock 3x-ui) treat
// every listed user as online and feed ips into LimitIP.
func TestGetUsersStatsOfflineIPsOnlyOnRequest(t *testing.T) {
	m, err := stats.NewManager(context.Background(), &stats.Config{})
	common.Must(err)
	connect(t, m, "short@example.com", "203.0.113.7", 100, 2000)
	disconnect(t, m, "short@example.com", "203.0.113.7")
	connect(t, m, "mixed@example.com", "198.51.100.1", 1, 2)
	connect(t, m, "mixed@example.com", "198.51.100.2", 5, 6)
	disconnect(t, m, "mixed@example.com", "198.51.100.2")
	s := NewStatsServer(m)

	legacy, err := s.GetUsersStats(context.Background(), &GetUsersStatsRequest{})
	common.Must(err)
	users := usersByEmail(legacy)
	if _, ok := users["short@example.com"]; ok {
		t.Fatal("user with no open connection listed to a caller that did not ask for offline IPs")
	}
	if got := users["mixed@example.com"]; len(got.Ips) != 1 || got.Ips[0].Ip != "198.51.100.1" || len(got.OfflineIps) != 0 {
		t.Fatalf("legacy view of mixed user = %v, want only the online IP", got)
	}

	full, err := s.GetUsersStats(context.Background(), &GetUsersStatsRequest{IncludeOfflineIps: true})
	common.Must(err)
	users = usersByEmail(full)
	short := users["short@example.com"]
	if short == nil || len(short.Ips) != 0 || len(short.OfflineIps) != 1 {
		t.Fatalf("short-lived user = %v, want one offline IP and no online IP", short)
	}
	if e := short.OfflineIps[0]; e.Ip != "203.0.113.7" || e.GetUplink() != 100 || e.GetDownlink() != 2000 {
		t.Fatalf("offline entry = %v, want 203.0.113.7 100/2000", e)
	}
	mixed := users["mixed@example.com"]
	if len(mixed.Ips) != 1 || len(mixed.OfflineIps) != 1 || mixed.OfflineIps[0].GetUplink() != 5 {
		t.Fatalf("mixed user = %v, want one online and one offline (5/6) IP", mixed)
	}
}

// Resetting the account counters must not reset the per-IP bytes: those are
// cumulative and the caller turns them into deltas.
func TestGetUsersStatsResetLeavesIPBytesCumulative(t *testing.T) {
	m, err := stats.NewManager(context.Background(), &stats.Config{})
	common.Must(err)
	connect(t, m, "a@example.com", "203.0.113.7", 100, 2000)
	s := NewStatsServer(m)

	_, err = s.GetUsersStats(context.Background(), &GetUsersStatsRequest{IncludeTraffic: true, Reset_: true})
	common.Must(err)
	resp, err := s.GetUsersStats(context.Background(), &GetUsersStatsRequest{})
	common.Must(err)
	if e := usersByEmail(resp)["a@example.com"].Ips[0]; e.GetUplink() != 100 || e.GetDownlink() != 2000 {
		t.Fatalf("after reset entry = %v, want still 100/2000", e)
	}
}

// An IP the core does not count bytes for has the fields unset, not 0, so the
// caller can tell "no data" from "no traffic".
func TestGetUsersStatsBytesUnsetWhenNotCounted(t *testing.T) {
	m, err := stats.NewManager(context.Background(), &stats.Config{})
	common.Must(err)
	connect(t, m, "a@example.com", "203.0.113.7", 0, 0)
	om, err := m.GetOrRegisterOnlineMap("user>>>a@example.com>>>online")
	common.Must(err)
	om.AddIP("198.51.100.9") // plain AddIP: online, no byte accounting

	resp, err := NewStatsServer(m).GetUsersStats(context.Background(), &GetUsersStatsRequest{})
	common.Must(err)
	ips := entriesByIP(usersByEmail(resp)["a@example.com"].Ips)
	if e := ips["203.0.113.7"]; e.Uplink == nil || e.Downlink == nil || *e.Uplink != 0 {
		t.Fatalf("counted IP with no traffic = %v, want uplink/downlink set to 0", e)
	}
	if e := ips["198.51.100.9"]; e.Uplink != nil || e.Downlink != nil {
		t.Fatalf("uncounted IP = %v, want uplink/downlink unset", e)
	}
}
