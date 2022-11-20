package failover

import (
	"context"

	"github.com/coredns/coredns/plugin"
	"github.com/miekg/dns"
)

type Failover struct {
	Next         plugin.Handler
	HealthChecks []*Health
}

// ServeDNS implements the plugin.Handler interface.
func (fo Failover) ServeDNS(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	frw := &FailoverResponseWriter{
		ResponseWriter: w,
		healthChecks:   fo.HealthChecks,
	}
	return plugin.NextOrFailure(fo.Name(), fo.Next, ctx, frw, r)
}

// Name implements the Handler interface.
func (fo Failover) Name() string { return "failover" }
