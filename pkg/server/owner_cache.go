package server

import (
	"sync"
	"time"

	"github.com/LosFurina/tmuxatlas/pkg/tmux"
)

const (
	ownerCacheTTL = 3 * time.Second
	ownerRate     = 32.0 // owner calls per second and per host, sustained
	ownerBurst    = 64.0
	ownerMaxKeys  = 4096
)

// ownerCache coalesces and briefly caches "owner" calls to peers, and rate
// limits them per host. A peer that answers slowly or not at all costs one
// call per session and per TTL, whoever asks; over the rate limit the
// answer is "unknown", which every caller treats as a refusal.
type ownerCache struct {
	ttl     time.Duration
	rate    float64
	burst   float64
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]*ownerEntry
	buckets map[string]*ownerBucket
}

type ownerEntry struct {
	ready chan struct{}
	at    time.Time
	owner tmux.Owner
	ok    bool
}

type ownerBucket struct {
	tokens float64
	at     time.Time
}

func newOwnerCache(ttl time.Duration, rate, burst float64) *ownerCache {
	return &ownerCache{ttl: ttl, rate: rate, burst: burst, now: time.Now,
		entries: make(map[string]*ownerEntry), buckets: make(map[string]*ownerBucket)}
}

func (c *ownerCache) allow(host string, now time.Time) bool {
	bucket := c.buckets[host]
	if bucket == nil {
		bucket = &ownerBucket{tokens: c.burst, at: now}
		c.buckets[host] = bucket
	}
	bucket.tokens = min(c.burst, bucket.tokens+now.Sub(bucket.at).Seconds()*c.rate)
	bucket.at = now
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

func (c *ownerCache) prune(now time.Time) {
	for key, entry := range c.entries {
		select {
		case <-entry.ready:
			if now.Sub(entry.at) >= c.ttl {
				delete(c.entries, key)
			}
		default:
		}
	}
}

// get returns the cached answer for host/name, waits for a call already in
// flight, or makes the call with fetch.
func (c *ownerCache) get(host, name string, fetch func() (tmux.Owner, bool)) (tmux.Owner, bool) {
	key := host + "\x00" + name
	c.mu.Lock()
	now := c.now()
	if entry := c.entries[key]; entry != nil {
		select {
		case <-entry.ready:
			if now.Sub(entry.at) < c.ttl {
				c.mu.Unlock()
				return entry.owner, entry.ok
			}
		default:
			c.mu.Unlock()
			<-entry.ready
			return entry.owner, entry.ok
		}
	}
	if !c.allow(host, now) {
		c.mu.Unlock()
		return "", false
	}
	if len(c.entries) >= ownerMaxKeys {
		c.prune(now)
	}
	entry := &ownerEntry{ready: make(chan struct{})}
	c.entries[key] = entry
	c.mu.Unlock()

	owner, ok := fetch()
	c.mu.Lock()
	entry.owner, entry.ok, entry.at = owner, ok, c.now()
	close(entry.ready)
	c.mu.Unlock()
	return owner, ok
}
