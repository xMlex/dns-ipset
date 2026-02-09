package cache

import (
	"strconv"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// SimpleCache — простая, но эффективная реализация интерфейса Cache.
type SimpleCache struct {
	mu       sync.RWMutex
	data     map[string]*Entry // ключ: Domain + "|" + ReqType
	stop     chan struct{}
	expireCh chan *NotifyExpire
}

// NewSimpleCache создаёт новый экземпляр SimpleCache.
func NewSimpleCache() *SimpleCache {
	cache := &SimpleCache{
		data:     make(map[string]*Entry),
		stop:     make(chan struct{}),
		expireCh: make(chan *NotifyExpire, 10), // буферизированный канал для уведомлений
	}
	go cache.startGC()
	return cache
}

func (c *SimpleCache) key(reqType uint16, domain string) string {
	return domain + "|" + strconv.Itoa(int(reqType))
}

// Get возвращает DNS-записи, если они не просрочены.
func (c *SimpleCache) Get(reqType uint16, domain string) *Entry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	key := c.key(reqType, domain)
	if entry, found := c.data[key]; found && time.Now().Before(entry.ExpiresAt) {
		return entry
	}
	return nil
}

// Set добавляет DNS-записи в кэш с указанным TTL.
func (c *SimpleCache) Set(reqType uint16, domain string, rr []dns.RR, ttl time.Duration) {
	key := c.key(reqType, domain)

	if ttl <= 0 {
		for _, r := range rr {
			if time.Duration(r.Header().Ttl) <= 0 {
				continue
			}
			if ttl <= 0 {
				ttl = time.Duration(r.Header().Ttl) * time.Second
			} else if time.Duration(r.Header().Ttl) < ttl {
				ttl = time.Duration(r.Header().Ttl) * time.Second
			}
		}
	}
	expiry := time.Now().Add(ttl)

	c.mu.Lock()
	c.data[key] = &Entry{
		Value:     rr,
		ExpiresAt: expiry,
		ReqType:   reqType,
		Domain:    domain,
	}
	c.mu.Unlock()
}

// NotifyExpire регистрирует получателя для уведомлений об истечении срока действия записей.
func (c *SimpleCache) NotifyExpire(receiver chan *NotifyExpire) chan *NotifyExpire {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Заменяем внутренний канал, чтобы передать его внешнему потребителю
	if receiver != nil {
		c.expireCh = receiver
	}
	return c.expireCh
}

// startGC запускает фоновую очистку просроченных записей.
func (c *SimpleCache) startGC() {
	ticker := time.NewTicker(time.Second * 30)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.gcOnce()
		case <-c.stop:
			return
		}
	}
}

// gcOnce проверяет и удаляет просроченные записи, отправляя уведомления.
func (c *SimpleCache) gcOnce() {
	now := time.Now()
	var expired []*Entry

	c.mu.Lock()
	for key, entry := range c.data {
		if now.After(entry.ExpiresAt) {
			expired = append(expired, entry)
			delete(c.data, key)
		}
	}
	c.mu.Unlock()

	// Отправляем уведомления без блокировки основного кэша
	for _, entry := range expired {
		select {
		case c.expireCh <- &NotifyExpire{
			ReqType: entry.ReqType,
			Domain:  entry.Domain,
		}:
		default: // Если канал полон — пропускаем, чтобы не блокировать GC
		}
	}
}

// Close останавливает фоновые процессы и освобождает ресурсы.
func (c *SimpleCache) Close() {
	close(c.stop)
	close(c.expireCh)
}
