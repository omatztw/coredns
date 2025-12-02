package snooper

import (
	"time"

	"github.com/coredns/caddy"
	"github.com/coredns/coredns/core/dnsserver"
	"github.com/coredns/coredns/plugin"
)

func init() {
	plugin.Register("snooper", setup)
}

func setup(c *caddy.Controller) error {
	s, err := parse(c)
	if err != nil {
		return plugin.Error("snooper", err)
	}

	// Register shutdown handler
	c.OnShutdown(func() error {
		if s.DB != nil {
			return s.DB.Close()
		}
		return nil
	})

	dnsserver.GetConfig(c).AddPlugin(func(next plugin.Handler) plugin.Handler {
		s.Next = next
		return s
	})

	return nil
}

func parse(c *caddy.Controller) (*Snooper, error) {
	s := &Snooper{}

	// Default PostgreSQL connection string
	connStr := "host=localhost port=5432 user=coredns dbname=coredns sslmode=disable"
	var cleanupInterval time.Duration
	var cleanupMaxAge time.Duration

	for c.Next() {
		// Get zones from same line as directive (e.g., "snooper example.org example.com")
		s.Zones = c.RemainingArgs()

		for c.NextBlock() {
			switch c.Val() {
			case "dsn":
				// PostgreSQL connection string
				// dsn "host=localhost port=5432 user=coredns password=secret dbname=coredns sslmode=disable"
				if !c.NextArg() {
					return nil, c.ArgErr()
				}
				connStr = c.Val()
			case "cleanup":
				// cleanup <interval> <max_age>
				// e.g., cleanup 1h 24h
				args := c.RemainingArgs()
				if len(args) != 2 {
					return nil, c.Errf("cleanup requires two arguments: interval and max_age")
				}
				var err error
				cleanupInterval, err = time.ParseDuration(args[0])
				if err != nil {
					return nil, c.Errf("invalid cleanup interval: %v", err)
				}
				cleanupMaxAge, err = time.ParseDuration(args[1])
				if err != nil {
					return nil, c.Errf("invalid cleanup max_age: %v", err)
				}
			default:
				return nil, c.Errf("unknown property '%s'", c.Val())
			}
		}
	}

	// Initialize database
	db, err := NewDB(connStr)
	if err != nil {
		return nil, c.Errf("failed to connect to database: %v", err)
	}
	s.DB = db

	// Start cleanup goroutine if configured
	if cleanupInterval > 0 && cleanupMaxAge > 0 {
		go func() {
			ticker := time.NewTicker(cleanupInterval)
			defer ticker.Stop()
			for range ticker.C {
				deleted, err := db.CleanupExpired(cleanupMaxAge)
				if err != nil {
					log.Errorf("cleanup failed: %v", err)
				} else if deleted > 0 {
					log.Infof("cleaned up %d expired records", deleted)
				}
			}
		}()
	}

	return s, nil
}
