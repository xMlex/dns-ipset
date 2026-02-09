package main

import (
	"crypto/tls"
	dnsCache "dns-ipset/pkg/cache"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/miekg/dns"
)

var DnsExchangeHandler *DnsHandler

type DnsHandler struct {
	clients           map[string][]*dns.Client
	failbackClients   map[string][]*dns.Client
	msgChan           chan *DnsExchangeMessage
	retryChan         chan *DnsExchangeMessage
	retryFailbackChan chan *DnsExchangeMessage
}
type DnsExchangeMessage struct {
	Message        *dns.Msg
	ReturnChan     chan *dns.Msg
	ReturnFailback chan *dns.Msg
	retryCount     int
}

func NewDnsHandler(NameServerAddrs []string, NameServersFailback []string) *DnsHandler {
	res := &DnsHandler{}
	res.msgChan = make(chan *DnsExchangeMessage, len(NameServerAddrs)*2)
	res.retryChan = make(chan *DnsExchangeMessage, 256)
	res.retryFailbackChan = make(chan *DnsExchangeMessage, 256)
	res.clients = make(map[string][]*dns.Client)
	res.failbackClients = make(map[string][]*dns.Client)
	res.runWorkerRetry()

	for _, srvAddr := range NameServerAddrs {
		runHandler(srvAddr, res.clients, res.runWorker)
	}
	for _, srvAddr := range NameServersFailback {
		runHandler(srvAddr, res.failbackClients, res.runWorkerFailback)
	}
	return res
}

func runHandler(srvAddr string, clients map[string][]*dns.Client, worker func(client *dns.Client, srvAddr string)) {
	net := "udp"
	addr := srvAddr
	tlsServerName := ""
	if idx := strings.Index(srvAddr, ":853"); idx != -1 {
		net = "tcp-tls"
		addr = srvAddr[:idx+4]
		tlsServerName = srvAddr[idx+5:]
	}
	clients[addr] = make([]*dns.Client, 1)
	for i := 0; i < len(clients[addr]); i++ {
		clients[addr][i] = &dns.Client{
			Net:          net,
			ReadTimeout:  time.Millisecond * 2000,
			WriteTimeout: time.Millisecond * 2000,
			TLSConfig: &tls.Config{
				ServerName:         tlsServerName,
				InsecureSkipVerify: false,
			},
		}
		worker(clients[addr][i], addr)
	}
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

func (h *DnsHandler) runWorkerFailback(client *dns.Client, srvAddr string) {
	go func() {
		for msg := range h.retryFailbackChan {
			in, _, err := client.Exchange(msg.Message, srvAddr)
			if err != nil {
				if msg.retryCount < config.RetryFailbackCount {
					msg.retryCount++
					select {
					case h.retryFailbackChan <- msg:
					default:
						log.Printf("DNS[%s] Exchange failback [error]: %s - retry skip", srvAddr, err)
					}
					continue
				}
				log.Printf("DNS[%s] Exchange failback [error]: %s - skip for %s after retry %d", srvAddr, err, msg.Message.Question[0].Name, msg.retryCount)
				continue
			}
			h.onAnswerDns(msg, in, srvAddr)
		}
	}()
}

func (h *DnsHandler) runWorker(client *dns.Client, srvAddr string) {
	go func() {
		for msg := range h.msgChan {
			in, _, err := client.Exchange(msg.Message, srvAddr)
			if err != nil {
				if msg.retryCount < config.RetryCount {
					log.Printf("DNS[%s] Exchange[%d]: %s for %s - retry", srvAddr, msg.retryCount+1, err, msg.Message.Question[0].Name)
					msg.retryCount++
					h.retryChan <- msg
				} else {
					msg.retryCount = 0
					select {
					case h.retryFailbackChan <- msg:
					default:
						log.Printf("DNS[%s] Exchange[error]: %s", srvAddr, err)
					}
				}
				continue
			}
			h.onAnswerDns(msg, in, srvAddr)
		}
	}()
}

func (h *DnsHandler) onAnswerDns(msg *DnsExchangeMessage, in *dns.Msg, srvAddr string) {
	if in == nil {
		return
	}
	if in.Rcode != dns.RcodeSuccess {
		if in.Rcode == dns.RcodeServerFailure {
			return
		}
	}

	addResolvedByAnswer(srvAddr, nil, in)

	select {
	case msg.ReturnChan <- in:
	default:
	}

	name := in.Question[0].Name
	err := ipSet.Set(name[:len(name)-1], in.Answer)
	if err != nil {
		fmt.Printf("failed to ipSet : %v\n", err)
	}
}

func cacheExpireHandle(cache dnsCache.Cache) {
	chExpire := make(chan *dnsCache.NotifyExpire)
	cache.NotifyExpire(chExpire)
	go func() {
		res := make(chan *dns.Msg, 1)
		req := new(dns.Msg)

		ticker := time.NewTicker(time.Second * 16)
		defer ticker.Stop()

		for {
			select {
			case exp, ok := <-chExpire:
				if !ok {
					return
				}
				req.SetQuestion(dns.Fqdn(exp.Domain), exp.ReqType)
				exchangeMsg := &DnsExchangeMessage{
					Message:        req,
					ReturnChan:     res,
					ReturnFailback: res,
				}
				DnsExchangeHandler.Handle(exchangeMsg)
				select {
				case r := <-res:
					cache.Set(exp.ReqType, exp.Domain, r.Answer, 0)
				case <-ticker.C:
					cache.Set(exp.ReqType, exp.Domain, req.Answer, 5)
				}
			}
		}
	}()
}
