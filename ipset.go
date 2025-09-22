package main

import (
	"log"
	"strings"
	"sync"

	"github.com/janeczku/go-ipset/ipset"
	"github.com/miekg/dns"
)

type IpSet interface {
	Set(domain string, ip []dns.RR) error
}

type BaseIpSet struct {
	locker sync.RWMutex
	sets   map[string]*ipset.IPSet
}

func NewIpSet() IpSet {
	return &BaseIpSet{
		sets: map[string]*ipset.IPSet{},
	}
}

func (c *BaseIpSet) Set(domain string, ipList []dns.RR) (err error) {
	c.locker.Lock()
	defer c.locker.Unlock()

	for setName, set := range config.IpSets {

		if _, ok := c.sets[setName]; !ok {
			s, _ := ipset.New(setName, "hash:ip", ipset.Params{})
			c.sets[setName] = s
		}
		for _, setDomain := range set {
			if strings.HasSuffix(domain, setDomain) {
				for _, ip := range ipList {
					if ip.Header().Rrtype != dns.TypeA {
						continue
					}
					if config.Debug {
						log.Println("ipset add " + setName + " " + ip.(*dns.A).A.String() + "/32")
					}
					ttl := int(ip.Header().Ttl - 1)
					if ttl < 0 {
						ttl = 0
					}
					ttl += 30
					if ttl > 2147482 {
						ttl = 2147482
					}
					err = c.sets[setName].Add(ip.(*dns.A).A.String(), ttl)
					if err != nil {
						log.Println("c.sets[setName].Add(ip.(*dns.A).A.String(), int(ip.Header().Ttl-1)): ", err)
					}
				}
			}
		}
	}
	return nil
}
