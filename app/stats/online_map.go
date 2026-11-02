package stats

import (
	"cmp"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/features/stats"
)

const (
	localhostIPv4 = "127.0.0.1"
	localhostIPv6 = "[::1]"

	// OfflineIPGrace is how long an IP with traffic counters is kept after its
	// last connection closed. It must exceed the API polling interval, and
	// matches the 30 minute IP retention of 3x-ui.
	OfflineIPGrace = 30 * time.Minute
	// MaxOfflineIPs bounds the offline entries kept per user, so that
	// CGNAT-style IP rotation cannot grow the map without limit inside the
	// grace period. Past it the entries that went offline first are dropped,
	// down to 7/8 of the cap so the scan is paid once per MaxOfflineIPs/8
	// disconnects.
	MaxOfflineIPs = 256
)

type ipEntry struct {
	refCount int
	lastSeen int64
	// offlineSince is the Unix time the last connection closed; meaningful
	// only while refCount is zero.
	offlineSince int64
	// traffic is non-nil for entries created by AddIPWithTraffic. Such entries
	// outlive their last connection by the offline grace period.
	traffic *ipTraffic
}

type ipTraffic struct {
	uplink   Counter
	downlink Counter
}

// OnlineMap is a refcount-based implementation of stats.OnlineMap.
// IPs are tracked by reference counting: AddIP increments, RemoveIP decrements.
// An IP is removed from the map when its reference count reaches zero, unless it
// carries traffic counters (AddIPWithTraffic): then it stays, offline, so its
// bytes can still be read.
type OnlineMap struct {
	entries map[string]*ipEntry
	access  sync.Mutex
	count   atomic.Int64
	// offline is the number of entries kept with refCount zero.
	offline int
	now     func() time.Time
}

// NewOnlineMap creates a new OnlineMap instance.
func NewOnlineMap() *OnlineMap {
	return &OnlineMap{
		entries: make(map[string]*ipEntry),
		now:     time.Now,
	}
}

// AddIP implements stats.OnlineMap.
func (om *OnlineMap) AddIP(ip string) {
	if ip == localhostIPv4 || ip == localhostIPv6 {
		return
	}
	om.access.Lock()
	defer om.access.Unlock()
	om.addLocked(ip)
}

// AddIPWithTraffic implements stats.IPTrafficOnlineMap.
func (om *OnlineMap) AddIPWithTraffic(ip string) (stats.Counter, stats.Counter) {
	if ip == localhostIPv4 || ip == localhostIPv6 {
		return nil, nil
	}
	om.access.Lock()
	defer om.access.Unlock()
	e := om.addLocked(ip)
	if e.traffic == nil {
		e.traffic = &ipTraffic{}
	}
	return &e.traffic.uplink, &e.traffic.downlink
}

func (om *OnlineMap) addLocked(ip string) *ipEntry {
	now := om.now().Unix()
	e, ok := om.entries[ip]
	if !ok {
		e = &ipEntry{}
		om.entries[ip] = e
	} else if e.refCount == 0 {
		om.offline--
	}
	if e.refCount == 0 {
		om.count.Add(1)
	}
	e.refCount++
	e.lastSeen = now
	return e
}

// RemoveIP implements stats.OnlineMap.
func (om *OnlineMap) RemoveIP(ip string) {
	om.access.Lock()
	defer om.access.Unlock()
	e, ok := om.entries[ip]
	if !ok || e.refCount <= 0 {
		return
	}
	e.refCount--
	if e.refCount > 0 {
		return
	}
	om.count.Add(-1)
	if e.traffic == nil {
		delete(om.entries, ip)
		return
	}
	now := om.now().Unix()
	e.offlineSince = now
	om.offline++
	if om.offline > MaxOfflineIPs {
		om.reclaimLocked(now)
		if om.offline > MaxOfflineIPs {
			om.evictOldestOfflineLocked(om.offline - (MaxOfflineIPs - MaxOfflineIPs/8))
		}
	}
}

// reclaimLocked drops the offline entries whose grace period has passed.
func (om *OnlineMap) reclaimLocked(now int64) {
	if om.offline == 0 {
		return
	}
	deadline := now - int64(OfflineIPGrace/time.Second)
	for ip, e := range om.entries {
		if e.refCount == 0 && e.offlineSince <= deadline {
			delete(om.entries, ip)
			om.offline--
		}
	}
}

// evictOldestOfflineLocked drops the n offline entries that went offline first.
func (om *OnlineMap) evictOldestOfflineLocked(n int) {
	type offlineIP struct {
		ip    string
		since int64
	}
	offline := make([]offlineIP, 0, om.offline)
	for ip, e := range om.entries {
		if e.refCount == 0 {
			offline = append(offline, offlineIP{ip, e.offlineSince})
		}
	}
	slices.SortFunc(offline, func(a, b offlineIP) int { return cmp.Compare(a.since, b.since) })
	for _, o := range offline[:min(n, len(offline))] {
		delete(om.entries, o.ip)
		om.offline--
	}
}

// Count implements stats.OnlineMap.
func (om *OnlineMap) Count() int {
	return int(om.count.Load())
}

// ForEach calls fn for each online IP. If fn returns false, iteration stops.
func (om *OnlineMap) ForEach(fn func(string, int64) bool) {
	om.access.Lock()
	defer om.access.Unlock()
	om.reclaimLocked(om.now().Unix())
	for ip, e := range om.entries {
		if e.refCount <= 0 {
			continue
		}
		if !fn(ip, e.lastSeen) {
			break
		}
	}
}

// ForEachTraffic implements stats.IPTrafficOnlineMap.
func (om *OnlineMap) ForEachTraffic(fn func(stats.IPTraffic) bool) {
	om.access.Lock()
	defer om.access.Unlock()
	om.reclaimLocked(om.now().Unix())
	for ip, e := range om.entries {
		t := stats.IPTraffic{IP: ip, LastSeen: e.lastSeen, Online: e.refCount > 0}
		if e.traffic != nil {
			t.Counted = true
			t.Uplink = e.traffic.uplink.Value()
			t.Downlink = e.traffic.downlink.Value()
		}
		if !fn(t) {
			break
		}
	}
}
