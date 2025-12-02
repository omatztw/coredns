// Package snooper implements a CoreDNS plugin that snoops DNS A record responses
// and stores FQDN to IP mappings in a SQLite database for later use (e.g., LBO routing).
package snooper

import (
	"context"
	"strings"

	"github.com/coredns/coredns/plugin"
	clog "github.com/coredns/coredns/plugin/pkg/log"
	"github.com/coredns/coredns/request"

	"github.com/miekg/dns"
)

var log = clog.NewWithPlugin("snooper")

// Snooper is a plugin that snoops DNS responses and records A record mappings.
type Snooper struct {
	Next plugin.Handler
	DB   *DB

	// Configuration
	Zones []string // Zones to snoop (empty = all zones)
}

// Name implements the plugin.Handler interface.
func (s *Snooper) Name() string { return "snooper" }

// ServeDNS implements the plugin.Handler interface.
func (s *Snooper) ServeDNS(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	// Wrap the ResponseWriter to intercept responses
	sw := &snoopResponseWriter{
		ResponseWriter: w,
		snooper:        s,
		request:        r,
	}

	return plugin.NextOrFailure(s.Name(), s.Next, ctx, sw, r)
}

// snoopResponseWriter wraps dns.ResponseWriter to intercept WriteMsg calls.
type snoopResponseWriter struct {
	dns.ResponseWriter
	snooper *Snooper
	request *dns.Msg
}

// WriteMsg intercepts DNS responses to extract and store A record mappings.
func (sw *snoopResponseWriter) WriteMsg(res *dns.Msg) error {
	if res == nil {
		return sw.ResponseWriter.WriteMsg(res)
	}

	// Only process successful responses
	if res.Rcode != dns.RcodeSuccess {
		return sw.ResponseWriter.WriteMsg(res)
	}

	// Check if we should snoop this zone
	if len(sw.snooper.Zones) > 0 {
		state := request.Request{W: sw.ResponseWriter, Req: sw.request}
		zone := plugin.Zones(sw.snooper.Zones).Matches(state.Name())
		if zone == "" {
			return sw.ResponseWriter.WriteMsg(res)
		}
	}

	// Extract A records from answer section
	sw.extractAndStore(res.Answer)

	// Also check additional section (often contains glue records)
	sw.extractAndStore(res.Extra)

	return sw.ResponseWriter.WriteMsg(res)
}

// extractAndStore extracts A records from RR slice and stores them in DB.
func (sw *snoopResponseWriter) extractAndStore(rrs []dns.RR) {
	var records []DNSRecord

	for _, rr := range rrs {
		switch r := rr.(type) {
		case *dns.A:
			fqdn := normalizeFQDN(r.Hdr.Name)
			ip := r.A.String()
			ttl := r.Hdr.Ttl

			records = append(records, DNSRecord{
				FQDN: fqdn,
				IP:   ip,
				TTL:  ttl,
			})

			log.Debugf("snooped A record: %s -> %s (TTL: %d)", fqdn, ip, ttl)
		}
	}

	if len(records) > 0 {
		if err := sw.snooper.DB.UpsertBatch(records); err != nil {
			log.Errorf("failed to store DNS records: %v", err)
		}
	}
}

// Write implements the dns.ResponseWriter interface.
func (sw *snoopResponseWriter) Write(buf []byte) (int, error) {
	// For raw writes, we can't easily intercept, so just pass through
	return sw.ResponseWriter.Write(buf)
}

// normalizeFQDN removes trailing dot and converts to lowercase.
func normalizeFQDN(fqdn string) string {
	fqdn = strings.ToLower(fqdn)
	return strings.TrimSuffix(fqdn, ".")
}
