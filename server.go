package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/miekg/dns"
)

func parseQuery(m *dns.Msg) {
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
						addResolvedByAnswer("config", err, name, m)
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
			m.Answer = cachedReq
			addResolvedByAnswer("cache", nil, q.Name, m)
			continue
		}

		r, err := Lookup(m)
		if err == nil {
			m.Answer = r.Answer
			cache.Set(q.Qtype, q.Name, m.Answer, 0)
			_ = ipSet.Set(q.Name[:len(q.Name)-1], m.Answer)
			if err != nil {
				fmt.Printf("failed to ipSet : %v\n", err)
			}
			fmt.Printf("ok %s\n", q.Name)
		} else {
			fmt.Printf("failed to exchange: %v\n", err)
		}
	}
}

func handleDnsRequest(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	parseQuery(m)
	_ = w.WriteMsg(m)
}

func Lookup(m *dns.Msg) (*dns.Msg, error) {

	req := new(dns.Msg)
	req.SetReply(m)
	req.Response = false

	qName := req.Question[0].Name

	res := make(chan *dns.Msg, 1)

	exchangeMsg := &DnsExchangeMessage{
		Message:    req,
		ReturnChan: res,
	}
	DnsExchangeHandler.Handle(exchangeMsg)

	ticker := time.NewTicker(time.Millisecond * 6300)
	defer ticker.Stop()

	select {
	case r := <-res:
		for _, rr := range r.Answer {
			rr.Header().Ttl = 10
		}
		return r, nil
	case <-ticker.C:
		return nil, errors.New("[lookup] can't resolve ip for " + qName + " by timeout")
	}
}

func addResolvedByAnswer(nameserver string, err error, qName string, r *dns.Msg) {
	rr, err := dns.NewRR(fmt.Sprintf("%s TXT %s", "dns.resolved.via", nameserver))
	if err != nil {
		rr.Header().Ttl = 15
	}
	//for i, rrA := range r.Answer {
	//	if rrA.Header().Ttl > 600 {
	//		r.Answer[i].Header().Ttl = 600
	//	}
	//	r.Answer[i].Header().Ttl += 10
	//}
	r.Answer = append(r.Answer, rr)
}
