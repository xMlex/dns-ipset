package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

var dnsMsgPool = sync.Pool{New: func() any { return &dns.Msg{} }}

// parseQuery parses the questions in m and populates m.Answer.
//
// For each question it will:
// - For A queries, check the local config.Address map for a matching name suffix
//   and, if found, synthesize an A RR (TTL=30) using the configured IP.
// - If not handled by config, try to serve from the cache.
// - Otherwise perform a DNS Lookup and, on success, set m.Answer to the lookup
//   response and store it in the cache (cached with TTL parameter 0).
//
// On function exit a deferred routine calls ipSet.Set for each question using the
// question name without the trailing dot and the final m.Answer; any ipSet errors
// are logged.
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
		} else {
			fmt.Printf("failed to exchange: %v\n", err)
		}
	}
}

// handleDnsRequest handles an incoming DNS request by creating a reply message,
// populating it via parseQuery, and sending it back to the client.
//
// The reply message is based on the request `r` (SetReply), parseQuery mutates
// the reply's sections (e.g., Answer), and the response is written to `w`.
// Any error returned by WriteMsg is ignored.
func handleDnsRequest(w dns.ResponseWriter, r *dns.Msg) {
	m := &dns.Msg{}
	m.SetReply(r)
	parseQuery(m)
	_ = w.WriteMsg(m)
}

// Lookup sends a DNS query derived from m to the DnsExchangeHandler and waits for a reply.
// It constructs a request based on m (marks it as a non-response), forwards it to the
// DnsExchangeHandler, and blocks up to 6 seconds for a response on the handler's return channel.
// The function uses the first question in the message for error reporting and returns either
// the received *dns.Msg or an error when the wait times out (error text is prefixed with "[lookup]").
func Lookup(m *dns.Msg) (*dns.Msg, error) {
	req := &dns.Msg{}

	req.SetReply(m)
	req.Response = false

	qName := req.Question[0].Name

	res := make(chan *dns.Msg, 1)

	exchangeMsg := &DnsExchangeMessage{
		Message:    req,
		ReturnChan: res,
	}
	DnsExchangeHandler.Handle(exchangeMsg)

	ticker := time.NewTicker(time.Millisecond * 6000)
	defer ticker.Stop()

	select {
	case r := <-res:
		return r, nil
	case <-ticker.C:
		return nil, errors.New("[lookup] can't resolve ip for " + qName + " by timeout")
	}
}

// addResolvedByAnswer appends a zero-TTL TXT record "dns.resolved.via" with the given
// nameserver to r.Answer, marking which nameserver resolved the query. The err and
// qName parameters are not used by this function.
func addResolvedByAnswer(nameserver string, err error, qName string, r *dns.Msg) {
	rr, err := dns.NewRR(fmt.Sprintf("%s TXT %s", "dns.resolved.via", nameserver))
	rr.Header().Ttl = 0
	r.Answer = append(r.Answer, rr)
}
