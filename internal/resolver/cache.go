package resolver

import (
	"sync"
	"time"

	"github.com/miekg/dns"
)

type cacheEntry struct {
	message   *dns.Msg
	inserted  time.Time
	expiresAt time.Time
}

type messageCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	maxTTL  time.Duration
	maxSize int
	now     func() time.Time
}

func newMessageCache(maxTTL time.Duration, maxSize int) *messageCache {
	return &messageCache{entries: make(map[string]cacheEntry), maxTTL: maxTTL, maxSize: maxSize, now: time.Now}
}

func (c *messageCache) get(key string, id uint16) (*dns.Msg, bool) {
	if c.maxTTL <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || !c.now().Before(entry.expiresAt) {
		delete(c.entries, key)
		return nil, false
	}
	result := entry.message.Copy()
	result.Id = id
	age := uint32(c.now().Sub(entry.inserted) / time.Second)
	for _, section := range [][]dns.RR{result.Answer, result.Ns, result.Extra} {
		for _, rr := range section {
			if rr.Header().Rrtype == dns.TypeOPT {
				continue
			}
			if rr.Header().Ttl > age {
				rr.Header().Ttl -= age
			} else {
				rr.Header().Ttl = 0
			}
		}
	}
	return result, true
}

func (c *messageCache) put(key string, message *dns.Msg) {
	if c.maxTTL <= 0 || c.maxSize <= 0 || message.Truncated {
		return
	}
	ttl, ok := minimumTTL(message)
	if !ok || ttl == 0 {
		return
	}
	duration := time.Duration(ttl) * time.Second
	if duration > c.maxTTL {
		duration = c.maxTTL
	}
	now := c.now()
	c.mu.Lock()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.maxSize {
		c.evictOne(now)
	}
	c.entries[key] = cacheEntry{message: message.Copy(), inserted: now, expiresAt: now.Add(duration)}
	c.mu.Unlock()
}

func (c *messageCache) evictOne(now time.Time) {
	var oldestKey string
	var oldestTime time.Time
	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
			return
		}
		if oldestKey == "" || entry.inserted.Before(oldestTime) {
			oldestKey = key
			oldestTime = entry.inserted
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

func minimumTTL(message *dns.Msg) (uint32, bool) {
	var ttl uint32
	found := false
	for _, section := range [][]dns.RR{message.Answer, message.Ns, message.Extra} {
		for _, rr := range section {
			if rr.Header().Rrtype == dns.TypeOPT {
				continue
			}
			if !found || rr.Header().Ttl < ttl {
				ttl = rr.Header().Ttl
				found = true
			}
		}
	}
	return ttl, found
}
