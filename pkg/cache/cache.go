package cache

import (
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

// NotifyExpire event for expired DNS record.
type NotifyExpire struct {
	ReqType uint16
	Domain  string
}

// Cache interface for DNS record cache.
type Cache interface {
	Get(reqType uint16, domain string) []dns.RR
	Set(reqType uint16, domain string, rr []dns.RR, ttl time.Duration)
	NotifyExpire(receiver chan *NotifyExpire) chan *NotifyExpire
	Close()
}

// internal implementation

type entry struct {
	value      []dns.RR
	expiresAt  time.Time
	accessAt   atomic.Int64 // unixNano last access time
	lastNotify int64        // unixNano of last notify; guarded via atomic
}

func (e *entry) updateAccess(now time.Time) {
	e.accessAt.Store(now.UnixNano())
}

// impl is the concrete sharded cache implementing Cache.
type impl struct {
	shards        []*cacheShard
	shardMask     uint32
	notifyInt     time.Duration
	janitorInt    time.Duration
	maxExpiredAge time.Duration
	minExpiredAge time.Duration
	mt            sync.Mutex

	stopJanitor   chan struct{}
	notifyExpires []chan *NotifyExpire
}

type cacheShard struct {
	mu sync.RWMutex
	m  map[string]*entry
}

// Options controls cache behaviour.
type Options struct {
	Shards          uint32        // number of shards; must be power of two; default 256
	NotifyInterval  time.Duration // min interval between expiry notifications per entry (default 5s)
	JanitorInterval time.Duration // how often janitor runs (default 1m)
	MaxExpiredAge   time.Duration // how long an entry may stay expired before janitor deletes it (default 10m)
	MinExpiredAge   time.Duration
}

const (
	defaultShards          = 256
	defaultNotifyInterval  = 5 * time.Second
	defaultJanitorInterval = 1 * time.Minute
	defaultMaxExpiredAge   = 60 * time.Minute
	defaultMinExpiredAge   = 5 * time.Minute
)

// New returns a new Cache implementation configured by opts.
// 
// Options are normalized and defaults applied when zero or negative:
//
// - Shards is rounded up to the next power of two (default 256).
// - NotifyInterval defaults to 5s.
// - JanitorInterval defaults to 1m.
// - MaxExpiredAge defaults to 60m.
// - MinExpiredAge defaults to 5m.
//
// The returned Cache uses sharded in-memory storage, starts a background
// janitor goroutine to purge old entries, and begins accepting Set/Get calls
// immediately. Call Close on the Cache to stop the janitor and release resources.
func New(opts Options) Cache {
	shards := opts.Shards
	if shards == 0 {
		shards = defaultShards
	}
	if (shards & (shards - 1)) != 0 {
		n := uint32(1)
		for n < shards {
			n <<= 1
		}
		shards = n
	}
	notify := opts.NotifyInterval
	if notify <= 0 {
		notify = defaultNotifyInterval
	}
	janitor := opts.JanitorInterval
	if janitor <= 0 {
		janitor = defaultJanitorInterval
	}
	maxAge := opts.MaxExpiredAge
	if maxAge <= 0 {
		maxAge = defaultMaxExpiredAge
	}
	minAge := opts.MinExpiredAge
	if minAge <= 0 {
		maxAge = defaultMinExpiredAge
	}

	c := &impl{
		shards:        make([]*cacheShard, shards),
		shardMask:     shards - 1,
		notifyInt:     notify,
		janitorInt:    janitor,
		maxExpiredAge: maxAge,
		minExpiredAge: minAge,
		stopJanitor:   make(chan struct{}),
	}
	for i := range c.shards {
		c.shards[i] = &cacheShard{m: make(map[string]*entry)}
	}
	go c.janitorLoop()
	return c
}

// makeKey returns the cache map key for the given DNS request type and domain.
// The key is the domain followed by ':' and the reqType encoded as a single rune (converted to a string).
// Example: domain "example.com" and reqType 1 produce "example.com:<rune(1)>".
func makeKey(reqType uint16, domain string) string {
	return domain + ":" + string(rune(reqType))
}

func (c *impl) shardFor(key string) *cacheShard {
	h := fnv.New32a()
	h.Write([]byte(key))
	idx := h.Sum32() & c.shardMask
	return c.shards[idx]
}

func (c *impl) Set(reqType uint16, domain string, rr []dns.RR, ttl time.Duration) {
	if len(rr) == 0 {
		return
	}

	var expires time.Time
	var ttlResult time.Duration

	if ttl > 0 {
		ttlResult = ttl
	} else {
		minTtl := rr[0].Header().Ttl
		for _, d := range rr {
			if d.Header().Ttl > 0 && d.Header().Ttl < minTtl {
				minTtl = d.Header().Ttl
			}
		}
		ttlResult = time.Duration(minTtl) * time.Second
	}
	if ttlResult <= 0 {
		ttlResult = c.minExpiredAge
	}

	expires = time.Now().Add(ttlResult)

	e := &entry{value: rr, expiresAt: expires}
	e.updateAccess(time.Now())
	key := makeKey(reqType, domain)
	sh := c.shardFor(key)
	sh.mu.Lock()
	sh.m[key] = e
	sh.mu.Unlock()
}

func (c *impl) Get(reqType uint16, domain string) []dns.RR {
	now := time.Now()
	key := makeKey(reqType, domain)
	sh := c.shardFor(key)
	sh.mu.RLock()
	e, ok := sh.m[key]
	sh.mu.RUnlock()
	if !ok {
		return nil
	}
	e.updateAccess(now)
	for i, _ := range e.value {
		e.value[i].Header().Ttl = uint32(e.expiresAt.Sub(now).Seconds())
	}
	if !e.expiresAt.IsZero() && now.After(e.expiresAt) {
		c.maybeNotify(reqType, domain, e, now)
	}
	return e.value
}

func (c *impl) maybeNotify(reqType uint16, domain string, e *entry, now time.Time) {
	last := atomic.LoadInt64(&e.lastNotify)
	if now.UnixNano()-last < c.notifyInt.Nanoseconds() {
		return
	}
	if atomic.CompareAndSwapInt64(&e.lastNotify, last, now.UnixNano()) {
		ev := &NotifyExpire{ReqType: reqType, Domain: domain}
		c.mt.Lock()
		defer c.mt.Unlock()
		for i, _ := range e.value {
			if e.value[i].Header().Ttl < 20 {
				e.value[i].Header().Ttl = 20
			}
		}
		for _, sh := range c.notifyExpires {
			select {
			case sh <- ev:
			default:
			}
		}
	}
}

func (c *impl) NotifyExpire(receiver chan *NotifyExpire) chan *NotifyExpire {
	c.mt.Lock()
	defer c.mt.Unlock()
	c.notifyExpires = append(c.notifyExpires, receiver)
	return receiver
}

func (c *impl) runJanitor() {
	now := time.Now()
	threshold := now.Add(-c.maxExpiredAge)
	for _, sh := range c.shards {
		sh.mu.Lock()
		for k, e := range sh.m {
			if e.expiresAt.IsZero() {
				continue
			}
			accessTime := time.Unix(0, e.accessAt.Load())
			if accessTime.After(threshold) {
				continue
			}
			if e.expiresAt.Before(threshold) {
				delete(sh.m, k)
			}
		}
		sh.mu.Unlock()
	}
}

func (c *impl) janitorLoop() {
	ticker := time.NewTicker(c.janitorInt)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.runJanitor()
		case <-c.stopJanitor:
			return
		}
	}
}

func (c *impl) Close() {
	close(c.stopJanitor)
}
