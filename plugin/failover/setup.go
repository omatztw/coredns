package failover

import (
	"net"
	"strconv"
	"time"

	"github.com/coredns/caddy"
	"github.com/coredns/coredns/core/dnsserver"
	"github.com/coredns/coredns/plugin"
)

func init() { plugin.Register("failover", setup) }

func setup(c *caddy.Controller) error {
	fo, err := failoverParse(c)
	if err != nil {
		return plugin.Error("failover", err)
	}

	c.OnStartup(func() error {
		for _, hc := range fo.HealthChecks {
			hc.Start()
		}
		return nil
	})

	c.OnShutdown(func() error {
		for _, hc := range fo.HealthChecks {
			hc.Stop()
		}
		return nil
	})

	dnsserver.GetConfig(c).AddPlugin(func(next plugin.Handler) plugin.Handler {
		fo.Next = next
		return fo
	})
	return nil
}

func failoverParse(c *caddy.Controller) (Failover, error) {
	fo := Failover{}

	for c.Next() {
		for c.NextBlock() {
			addr := net.ParseIP(c.Val())
			remainingTokens := c.RemainingArgs()
			ep := remainingTokens[0]
			interval, err := time.ParseDuration(remainingTokens[1])
			if err != nil {
				return fo, err
			}
			statusCode, err := strconv.Atoi(remainingTokens[2])
			if err != nil {
				return fo, err
			}
			maxFails, err := strconv.Atoi(remainingTokens[3])
			if err != nil {
				return fo, err
			}
			health := NewHealth(addr, ep, interval, statusCode, uint32(maxFails))
			fo.HealthChecks = append(fo.HealthChecks, health)
		}
	}

	return fo, nil
}
