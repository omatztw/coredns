package failover

import (
	"net"

	"github.com/miekg/dns"
)

// FailoverResponseWriter is a response writer that filter unhealthy endpoint.
type FailoverResponseWriter struct {
	dns.ResponseWriter
	healthChecks []*Health
}

// WriteMsg implements the dns.ResponseWriter interface.
func (f *FailoverResponseWriter) WriteMsg(res *dns.Msg) error {
	if res.Rcode != dns.RcodeSuccess {
		return f.ResponseWriter.WriteMsg(res)
	}

	if res.Question[0].Qtype == dns.TypeAXFR || res.Question[0].Qtype == dns.TypeIXFR {
		return f.ResponseWriter.WriteMsg(res)
	}

	res.Answer = f.filter(res.Answer)
	res.Ns = f.filter(res.Ns)
	res.Extra = f.filter(res.Extra)

	return f.ResponseWriter.WriteMsg(res)
}

func (f *FailoverResponseWriter) filter(in []dns.RR) []dns.RR {
	address := []dns.RR{}
	rest := []dns.RR{}

LOOP:
	for _, r := range in {
		rrType := r.Header().Rrtype
		switch rrType {
		case dns.TypeA, dns.TypeAAAA:
			ip := getAddress(r, rrType)
			for _, hc := range f.healthChecks {
				if !hc.GetStatus() && hc.addr.Equal(ip) {
					continue LOOP
				}
			}
			address = append(address, r)
		default:
			rest = append(rest, r)
		}
	}

	out := append(address, rest...)
	return out
}

func getAddress(rr dns.RR, rrType uint16) net.IP {
	if rrType == dns.TypeAAAA {
		return rr.(*dns.AAAA).AAAA
	}
	return rr.(*dns.A).A
}

// Write implements the dns.ResponseWriter interface.
func (r *FailoverResponseWriter) Write(buf []byte) (int, error) {
	n, err := r.ResponseWriter.Write(buf)
	return n, err
}
