package main

import (
	"github.com/miekg/dns"
	log "github.com/sirupsen/logrus"
	"net"
)

type DNSForwarder struct {
	server *dns.Server
}

type DNSRewriteForwardHandler struct {
	upstreamServer string
	redirectTo     string
}

func (D DNSRewriteForwardHandler) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	client := dns.Client{
		Net: "udp",
	}
	response, _, err := client.Exchange(r, D.upstreamServer+":53")
	if err != nil {
		log.Errorln("Error connecting to upstream: ", err)
		return
	}
	for _, rr := range response.Answer {
		switch rr.Header().Rrtype {
		case dns.TypeA:
			if t, ok := rr.(*dns.A); ok {
				if t.A.Equal(net.IPv4(127, 0, 0, 1)) {
					log.Debugln("Redirecting answer for ", t.Header().Name, "to ", D.redirectTo)
					t.A = net.ParseIP(D.redirectTo)
				}
			}
		}
	}
	err = w.WriteMsg(response)
	if err != nil {
		log.Errorln("Error writing response: ", err)
	}
}

func NewDnsForwarder(upstreamServer string) (*DNSForwarder, error) {
	forwarder := &DNSForwarder{
		server: &dns.Server{
			Net: "udp",
			Handler: DNSRewriteForwardHandler{
				upstreamServer: upstreamServer,
				redirectTo:     upstreamServer,
			},
		},
	}
	return forwarder, nil
}

func (c *DNSForwarder) Start() {
	go func() {
		err := c.server.ListenAndServe()
		if err != nil {
			log.Errorln("Error starting DNS server: ", err)
		}
	}()
}

func (c *DNSForwarder) Shutdown() {
	err := c.server.Shutdown()
	if err != nil {
		log.Errorln("Error shutting down DNS server: ", err)
	}
}
