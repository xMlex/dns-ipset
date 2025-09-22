package main

import (
	dnsCache "dns-ipset/pkg/cache"
	"flag"
	"log"
	"strconv"

	"github.com/miekg/dns"
)

var configFile = flag.String("c", "config.yaml", "configuration file")
var config = &Config{}
var ipSet = NewIpSet()

// var cache = NewMemoryCache()
var cache dnsCache.Cache

func main() {
	cache = dnsCache.New(dnsCache.Options{})
	defer cache.Close()
	cacheExpireHandle(cache)

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
