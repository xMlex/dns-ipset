package cache

import (
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
	Get(reqType uint16, domain string) *Entry
	Set(reqType uint16, domain string, rr []dns.RR, ttl time.Duration)
	NotifyExpire(receiver chan *NotifyExpire) chan *NotifyExpire
	Close()
}

type Entry struct {
	Value      []dns.RR
	ExpiresAt  time.Time
	AccessAt   atomic.Int64 // unixNano last access time
	LastNotify int64        // unixNano of last notify; guarded via atomic
	ReqType    uint16
	Domain     string
}
