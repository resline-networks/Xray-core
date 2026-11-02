package stats

import (
	"fmt"
	"testing"
	"time"

	feature_stats "github.com/xtls/xray-core/features/stats"
)

func collectTraffic(om *OnlineMap) map[string]feature_stats.IPTraffic {
	out := map[string]feature_stats.IPTraffic{}
	om.ForEachTraffic(func(t feature_stats.IPTraffic) bool {
		out[t.IP] = t
		return true
	})
	return out
}

func TestOnlineMapTrafficSurvivesDisconnect(t *testing.T) {
	om := NewOnlineMap()
	up, down := om.AddIPWithTraffic("203.0.113.7")
	up.Add(100)
	down.Add(2000)
	om.RemoveIP("203.0.113.7")

	got := collectTraffic(om)["203.0.113.7"]
	if got.Online || got.Uplink != 100 || got.Downlink != 2000 {
		t.Fatalf("after disconnect got %+v, want offline 100/2000", got)
	}
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClockedMap() (*OnlineMap, *fakeClock) {
	clock := &fakeClock{t: time.Unix(1_761_800_000, 0)}
	om := NewOnlineMap()
	om.now = clock.now
	return om, clock
}

func TestOnlineMapOfflineIPReclaimedAfterGrace(t *testing.T) {
	om, clock := newClockedMap()
	up, _ := om.AddIPWithTraffic("203.0.113.7")
	up.Add(100)
	om.RemoveIP("203.0.113.7")

	clock.advance(29 * time.Minute)
	if _, ok := collectTraffic(om)["203.0.113.7"]; !ok {
		t.Fatal("offline IP reclaimed before the 30 minute grace period")
	}
	clock.advance(2 * time.Minute)
	if got, ok := collectTraffic(om)["203.0.113.7"]; ok {
		t.Fatalf("offline IP still present 31 minutes after disconnect: %+v", got)
	}
}

func TestOnlineMapReconnectWithinGraceKeepsCounting(t *testing.T) {
	om, clock := newClockedMap()
	up, _ := om.AddIPWithTraffic("203.0.113.7")
	up.Add(100)
	om.RemoveIP("203.0.113.7")

	clock.advance(20 * time.Minute)
	up, _ = om.AddIPWithTraffic("203.0.113.7")
	up.Add(50)
	om.RemoveIP("203.0.113.7")

	clock.advance(20 * time.Minute) // 40 minutes after the first disconnect
	got := collectTraffic(om)["203.0.113.7"]
	if got.Uplink != 150 {
		t.Fatalf("uplink after reconnect = %d, want 150 (cumulative)", got.Uplink)
	}
}

func TestOnlineMapOnlineIPNeverReclaimed(t *testing.T) {
	om, clock := newClockedMap()
	om.AddIPWithTraffic("203.0.113.7")
	clock.advance(24 * time.Hour)
	if got := collectTraffic(om)["203.0.113.7"]; !got.Online {
		t.Fatal("IP with an open connection was reclaimed")
	}
}

func TestOnlineMapOfflineIPsBoundedUnderRotation(t *testing.T) {
	om, clock := newClockedMap()
	// A user rotating through 10,000 CGNAT addresses inside one grace period.
	for i := 0; i < 10_000; i++ {
		ip := fmt.Sprintf("100.64.%d.%d", i/250, i%250+1)
		om.AddIPWithTraffic(ip)
		om.RemoveIP(ip)
		clock.advance(100 * time.Millisecond)
	}
	om.AddIPWithTraffic("203.0.113.7") // still connected

	got := collectTraffic(om)
	if len(got) > MaxOfflineIPs+1 {
		t.Fatalf("tracked %d IPs, want at most %d offline + 1 online", len(got), MaxOfflineIPs)
	}
	if _, ok := got["100.64.39.250"]; !ok {
		t.Fatal("most recent offline IP was evicted; eviction must drop the oldest")
	}
	if _, ok := got["100.64.0.1"]; ok {
		t.Fatal("oldest offline IP kept past the cap")
	}
	if !got["203.0.113.7"].Online {
		t.Fatal("online IP evicted by the offline cap")
	}
}

func TestOnlineMapOnlineViewIgnoresOfflineIPs(t *testing.T) {
	om := NewOnlineMap()
	om.AddIPWithTraffic("203.0.113.7")
	om.AddIPWithTraffic("203.0.113.8")
	om.RemoveIP("203.0.113.8")

	if om.Count() != 1 {
		t.Fatalf("Count() = %d, want 1 (offline IPs are not online)", om.Count())
	}
	var seen []string
	om.ForEach(func(ip string, _ int64) bool {
		seen = append(seen, ip)
		return true
	})
	if len(seen) != 1 || seen[0] != "203.0.113.7" {
		t.Fatalf("ForEach saw %v, want only the online IP", seen)
	}
}

func TestOnlineMapPlainAddIPStillDropsAtZero(t *testing.T) {
	om := NewOnlineMap()
	om.AddIP("203.0.113.7")
	om.RemoveIP("203.0.113.7")
	if n := len(collectTraffic(om)); n != 0 {
		t.Fatalf("plain AddIP entry kept after its last connection (%d entries)", n)
	}
}

func TestOnlineMapLoopbackNotTracked(t *testing.T) {
	om := NewOnlineMap()
	up, down := om.AddIPWithTraffic("127.0.0.1")
	if up != nil || down != nil || om.Count() != 0 {
		t.Fatal("loopback source must not be tracked")
	}
}
