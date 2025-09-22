package cache

import (
	"hash/fnv"
	"log/slog"
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
}

const (
	defaultShards          = 256
	defaultNotifyInterval  = 5 * time.Second
	defaultJanitorInterval = 1 * time.Minute
	defaultMaxExpiredAge   = 10 * time.Minute
)

// New creates new Cache implementation.
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

	c := &impl{
		shards:        make([]*cacheShard, shards),
		shardMask:     shards - 1,
		notifyInt:     notify,
		janitorInt:    janitor,
		maxExpiredAge: maxAge,
		stopJanitor:   make(chan struct{}),
	}
	for i := range c.shards {
		c.shards[i] = &cacheShard{m: make(map[string]*entry)}
	}
	go c.janitorLoop()
	return c
}

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
			if d.Header().Ttl < minTtl {
				minTtl = d.Header().Ttl
			}
		}
	}
	if ttlResult <= 0 {
		ttlResult = time.Second * 10
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
	if !e.expiresAt.IsZero() && now.After(e.expiresAt) {
		c.maybeNotify(reqType, domain, e, now)
	}
	return e.value
}

func (c *impl) maybeNotify(reqType uint16, domain string, e *entry, now time.Time) {
	last := atomic.LoadInt64(&e.lastNotify)
	if now.UnixNano()-last < c.notifyInt.Nanoseconds() {
		slog.Info("skip notify")
		return
	}
	if atomic.CompareAndSwapInt64(&e.lastNotify, last, now.UnixNano()) {
		ev := &NotifyExpire{ReqType: reqType, Domain: domain}
		c.mt.Lock()
		defer c.mt.Unlock()
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
