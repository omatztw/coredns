// Package snooper implements a CoreDNS plugin that snoops DNS A record responses
// and stores FQDN to IP mappings in a PostgreSQL database for later use (e.g., LBO routing).
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

	// Extract and store records with CNAME resolution
	sw.extractAndStoreWithCNAME(res)

	return sw.ResponseWriter.WriteMsg(res)
}

// extractAndStoreWithCNAME extracts A records and also maps the original QNAME
// through CNAME chains to the resolved IPs.
func (sw *snoopResponseWriter) extractAndStoreWithCNAME(res *dns.Msg) {
	// Get original query name
	var qname string
	if len(sw.request.Question) > 0 {
		qname = normalizeFQDN(sw.request.Question[0].Name)
	}

	// Build CNAME chain map: source -> target
	cnameMap := make(map[string]string)
	// Collect A records: name -> []IP
	aRecords := make(map[string][]ipWithTTL)

	// First pass: collect CNAMEs and A records from answer section
	for _, rr := range res.Answer {
		switch r := rr.(type) {
		case *dns.CNAME:
			src := normalizeFQDN(r.Hdr.Name)
			dst := normalizeFQDN(r.Target)
			cnameMap[src] = dst
			log.Debugf("snooped CNAME: %s -> %s", src, dst)

		case *dns.A:
			name := normalizeFQDN(r.Hdr.Name)
			aRecords[name] = append(aRecords[name], ipWithTTL{
				IP:  r.A.String(),
				TTL: r.Hdr.Ttl,
			})
			log.Debugf("snooped A record: %s -> %s (TTL: %d)", name, r.A.String(), r.Hdr.Ttl)
		}
	}

	// Also check additional section for glue records
	for _, rr := range res.Extra {
		if r, ok := rr.(*dns.A); ok {
			name := normalizeFQDN(r.Hdr.Name)
			aRecords[name] = append(aRecords[name], ipWithTTL{
				IP:  r.A.String(),
				TTL: r.Hdr.Ttl,
			})
		}
	}

	// Build final records to store
	var records []DNSRecord

	// Store direct A records
	for name, ips := range aRecords {
		for _, ip := range ips {
			records = append(records, DNSRecord{
				FQDN: name,
				IP:   ip.IP,
				TTL:  ip.TTL,
			})
		}
	}

	// Resolve CNAME chains and map original QNAME to IPs
	if qname != "" {
		// Follow CNAME chain from qname
		resolvedIPs := sw.resolveCNAMEChain(qname, cnameMap, aRecords, 10)
		for _, ip := range resolvedIPs {
			// Only add if qname is different from the A record's name
			// (avoid duplicates if qname directly has A record)
			if _, directA := aRecords[qname]; !directA {
				records = append(records, DNSRecord{
					FQDN: qname,
					IP:   ip.IP,
					TTL:  ip.TTL,
				})
				log.Debugf("snooped CNAME-resolved: %s -> %s (TTL: %d)", qname, ip.IP, ip.TTL)
			}
		}
	}

	// Store all records
	if len(records) > 0 {
		if err := sw.snooper.DB.UpsertBatch(records); err != nil {
			log.Errorf("failed to store DNS records: %v", err)
		}
	}
}

// ipWithTTL holds an IP address with its TTL
type ipWithTTL struct {
	IP  string
	TTL uint32
}

// resolveCNAMEChain follows CNAME chain and returns all resolved IPs
func (sw *snoopResponseWriter) resolveCNAMEChain(name string, cnameMap map[string]string, aRecords map[string][]ipWithTTL, maxDepth int) []ipWithTTL {
	if maxDepth <= 0 {
		return nil
	}

	// Check if this name has direct A records
	if ips, ok := aRecords[name]; ok {
		return ips
	}

	// Check if this name has a CNAME
	if target, ok := cnameMap[name]; ok {
		return sw.resolveCNAMEChain(target, cnameMap, aRecords, maxDepth-1)
	}

	return nil
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
