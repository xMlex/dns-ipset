package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/miekg/dns"
)

func parseQuery(m *dns.Msg) {
	defer func() {
		for _, q := range m.Question {
			err := ipSet.Set(q.Name[:len(q.Name)-1], m.Answer)
			if err != nil {
				fmt.Printf("failed to ipSet : %v\n", err)
			}
		}
	}()
	for _, q := range m.Question {
		//log.Printf("Query for %s as %d\n", q.Name, q.Qtype)
		processed := false
		switch q.Qtype {
		case dns.TypeA:
			for name, ip := range config.Address {
				// check q.Name has suffix name without last char in q.Name
				if strings.HasSuffix(q.Name[:len(q.Name)-1], name) {
					rr, err := dns.NewRR(fmt.Sprintf("%s A %s", q.Name, ip))
					if err == nil {
						rr.Header().Ttl = 30
						m.Answer = append(m.Answer, rr)
						addResolvedByAnswer("config", err, m)
						processed = true
						break
					}
				}
			}
		}
		if processed {
			continue
		}
		cachedReq := cache.Get(q.Qtype, q.Name)
		if cachedReq != nil {
			m.Answer = cachedReq.Value
			for i := 0; i < len(m.Answer); i++ {
				m.Answer[i].Header().Ttl = uint32(cachedReq.ExpiresAt.Sub(time.Now()).Seconds())
			}
			addResolvedByAnswer("cache", nil, m)
			continue
		}

		r, err := Lookup(m)
		if err == nil {
			m.Answer = r.Answer
			cache.Set(q.Qtype, q.Name, r.Answer, 0)
		}
	}
}

func handleDnsRequest(w dns.ResponseWriter, r *dns.Msg) {
	m := &dns.Msg{}
	m.SetReply(r)
	parseQuery(m)
	_ = w.WriteMsg(m)
}

func Lookup(m *dns.Msg) (*dns.Msg, error) {
	req := &dns.Msg{}

	req.SetReply(m)
	req.Response = false

	qName := req.Question[0].Name

	res := make(chan *dns.Msg, 1)

	exchangeMsg := &DnsExchangeMessage{
		Message:        req,
		ReturnChan:     res,
		ReturnFailback: res,
	}
	DnsExchangeHandler.Handle(exchangeMsg)

	ticker := time.NewTicker(time.Second * 16)
	defer ticker.Stop()

	select {
	case r := <-res:
		return r, nil
	case <-ticker.C:
		return nil, errors.New("[lookup] can't resolve ip for " + qName + " by timeout")
	}
}

func addResolvedByAnswer(name string, err error, r *dns.Msg) {
	rr, err := dns.NewRR(fmt.Sprintf("%s TXT %s", "dns.resolved.via", name))
	rr.Header().Ttl = 0
	r.Answer = append(r.Answer, rr)
}
