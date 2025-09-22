package main

import (
	"crypto/tls"
	dnsCache "dns-ipset/pkg/cache"
	"fmt"
	"log"
	"log/slog"
	"strings"
	"time"

	"github.com/miekg/dns"
)

var DnsExchangeHandler *DnsHandler

type DnsHandler struct {
	clients   map[string][]*dns.Client
	msgChan   chan *DnsExchangeMessage
	retryChan chan *DnsExchangeMessage
}
type DnsExchangeMessage struct {
	Message    *dns.Msg
	ReturnChan chan *dns.Msg
	retryCount int
}

// NewDnsHandler creates and returns a DnsHandler configured for the provided name server addresses.
// 
// It initializes internal channels and client maps, starts the retry worker, and spawns a worker
// goroutine for each configured dns client.
//
// NameServerAddrs is a slice of server addresses. Addresses containing the suffix ":853" are
// treated as DNS-over-TLS endpoints: the client `Net` is set to "tcp-tls", the address used for
// exchanges is truncated to include the port, and the substring after ":853" is used as the
// TLS ServerName. For all clients the read/write timeouts are set to 5s and TLS verification is
// enabled (InsecureSkipVerify=false).
//
// The returned DnsHandler is ready to accept DnsExchangeMessage values via its Handle method.
func NewDnsHandler(NameServerAddrs []string) *DnsHandler {
	res := &DnsHandler{}
	res.msgChan = make(chan *DnsExchangeMessage, len(NameServerAddrs)*2)
	res.retryChan = make(chan *DnsExchangeMessage, 256)
	res.clients = make(map[string][]*dns.Client)
	res.runWorkerRetry()

	for _, srvAddr := range NameServerAddrs {
		net := "udp"
		addr := srvAddr
		tlsServerName := ""
		if idx := strings.Index(srvAddr, ":853"); idx != -1 {
			net = "tcp-tls"
			addr = srvAddr[:idx+4]
			tlsServerName = srvAddr[idx+5:]
		}
		res.clients[addr] = make([]*dns.Client, 1)
		for i := 0; i < len(res.clients[addr]); i++ {
			res.clients[addr][i] = &dns.Client{
				Net:          net,
				ReadTimeout:  time.Millisecond * 5000,
				WriteTimeout: time.Millisecond * 5000,
				TLSConfig: &tls.Config{
					ServerName:         tlsServerName,
					InsecureSkipVerify: false,
				},
			}
			res.runWorker(res.clients[addr][i], addr)
		}
	}
	return res
}

func (h *DnsHandler) Handle(exchangeMessage *DnsExchangeMessage) {
	h.msgChan <- exchangeMessage
}

func (h *DnsHandler) runWorkerRetry() {
	go func() {
		for msg := range h.retryChan {
			select {
			case h.msgChan <- msg:
			}
		}
	}()
}

func (h *DnsHandler) runWorker(client *dns.Client, srvAddr string) {
	go func() {
		for msg := range h.msgChan {
			in, _, err := client.Exchange(msg.Message, srvAddr)
			if err != nil {
				if msg.retryCount < 3 {
					log.Printf("DNS[%s] Exchange error[%d]: %s for %s", srvAddr, msg.retryCount, err, msg.Message.Question[0].Name)
					msg.retryCount++
					h.retryChan <- msg
					slog.Info("Retry")
				} else {
					log.Printf("DNS[%s] Exchange[error]: %s", srvAddr, err)
				}
				continue
			}
			if in != nil && in.Rcode != dns.RcodeSuccess {
				if in.Rcode == dns.RcodeServerFailure {
					continue
				}
			}
			addResolvedByAnswer(srvAddr, err, in.Question[0].Name, in)
			msg.ReturnChan <- in

			name := in.Question[0].Name
			err = ipSet.Set(name[:len(name)-1], in.Answer)
			if err != nil {
				fmt.Printf("failed to ipSet : %v\n", err)
			}
		}
	}()
}

// cacheExpireHandle registers for cache expiration notifications and starts a goroutine
// that refreshes expired DNS entries.
//
// When the provided cache emits an expiration (via NotifyExpire), this function issues
// a fresh DNS query for the expired domain/type through the global DnsExchangeHandler
// and writes the resulting answers back into the cache. The refresh is performed
// asynchronously in a dedicated goroutine; each expiration is handled by sending a
// DnsExchangeMessage to DnsExchangeHandler and waiting for its response before calling
// cache.Set to update the entry. The function returns immediately after registering
// the expiration channel.
func cacheExpireHandle(cache dnsCache.Cache) {
	chExpire := make(chan *dnsCache.NotifyExpire)
	cache.NotifyExpire(chExpire)
	go func() {
		res := make(chan *dns.Msg, 1)
		req := new(dns.Msg)
		for {
			select {
			case exp, ok := <-chExpire:
				if !ok {
					return
				}
				req.SetQuestion(dns.Fqdn(exp.Domain), exp.ReqType)
				exchangeMsg := &DnsExchangeMessage{
					Message:    req,
					ReturnChan: res,
				}
				DnsExchangeHandler.Handle(exchangeMsg)
				r := <-res
				cache.Set(exp.ReqType, exp.Domain, r.Answer, 0)
			}
		}
	}()
}
