package cache

import (
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestCacheSetGet(t *testing.T) {
	c := New(Options{NotifyInterval: 10 * time.Millisecond}).(*impl)
	defer c.Close()

	rr, _ := dns.NewRR("example.com. 1 IN A 1.2.3.4")
	c.Set(dns.TypeA, "example.com.", []dns.RR{rr}, 50*time.Millisecond)
	got := c.Get(dns.TypeA, "example.com.")
	if got == nil || got[0].String() != rr.String() {
		t.Fatalf("expected %v, got %v", rr, got)
	}
}

func TestCacheExpireNotify(t *testing.T) {
	c := New(Options{NotifyInterval: 10 * time.Millisecond}).(*impl)
	defer c.Close()

	rr, _ := dns.NewRR("expired.com. 1 IN A 1.1.1.1")
	c.Set(dns.TypeA, "expired.com.", []dns.RR{rr}, 1*time.Millisecond)
	receiver := make(chan *NotifyExpire, 1)
	c.NotifyExpire(receiver)

	time.Sleep(2 * time.Millisecond)
	_ = c.Get(dns.TypeA, "expired.com.")

	select {
	case ev := <-receiver:
		if ev.Domain != "expired.com." || ev.ReqType != dns.TypeA {
			t.Fatalf("unexpected notify %+v", ev)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("expected notification")
	}
}

func BenchmarkCacheContention(b *testing.B) {
	c := New(Options{}).(*impl)
	defer c.Close()

	rr, _ := dns.NewRR("bench.com. 60 IN A 9.9.9.9")
	c.Set(dns.TypeA, "bench.com.", []dns.RR{rr}, time.Minute)

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = c.Get(dns.TypeA, "bench.com.")
		}
	})
}
