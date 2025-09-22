package main

import (
	dnsCache "dns-ipset/pkg/cache"
	"flag"
	"github.com/miekg/dns"
	"log"
	"log/slog"
	"strconv"
)

var configFile = flag.String("c", "config.yaml", "configuration file")
var config = &Config{}
var ipSet = NewIpSet()

// var cache = NewMemoryCache()
var cache dnsCache.Cache

func main() {
	cache = dnsCache.New(dnsCache.Options{})
	chExpire := make(chan *dnsCache.NotifyExpire, 10)
	cache.NotifyExpire(chExpire)
	go func() {
		for {
			select {
			case exp := <-chExpire:
				slog.Info("expire expired " + exp.Domain)
				res := make(chan *dns.Msg, 1)
				req := new(dns.Msg)
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

	flag.Parse()
	var err error
	config, err = loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	// attach request handler func
	dns.HandleFunc(".", handleDnsRequest)

	DnsExchangeHandler = NewDnsHandler(config.Nameservers)

	// start server
	//port := 5354
	port := 53
	if config.Port > 1023 {
		port = int(config.Port)
	}
	server := &dns.Server{Addr: config.Host + ":" + strconv.Itoa(port), Net: "udp"}
	log.Printf("Starting at %d\n", port)
	err = server.ListenAndServe()
	defer server.Shutdown()
	if err != nil {
		log.Fatalf("Failed to start server: %s\n ", err.Error())
	}
}
