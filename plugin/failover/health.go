package failover

import (
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

type Health struct {
	addr       net.IP
	ep         string
	fails      uint32
	max_fails  uint32
	interval   time.Duration
	statusCode int
	stop       chan bool
	ticker     *time.Ticker
}

func NewHealth(addr net.IP, ep string, interval time.Duration, statusCode int, maxFails uint32) *Health {
	stop := make(chan bool)
	return &Health{
		addr:       addr,
		ep:         ep,
		interval:   interval,
		statusCode: statusCode,
		max_fails:  maxFails,
		fails:      0,
		stop:       stop,
	}
}

func (h *Health) check() error {
	client := http.Client{
		Timeout: 3 * time.Second,
	}
	resp, err := client.Get(h.ep)
	if err != nil {
		return err
	}
	if resp.StatusCode != h.statusCode {
		return fmt.Errorf("bad status: %d", resp.StatusCode)
	}
	return nil
}

func (h *Health) Start() {
	// Check once immediately after start
	if err := h.check(); err != nil {
		atomic.AddUint32(&h.fails, 1)
	}
	h.ticker = time.NewTicker(h.interval)
	h.stop = make(chan bool)
	go func() {
		for {
			select {
			case <-h.stop:
				return
			case <-h.ticker.C:
				if err := h.check(); err != nil {
					atomic.AddUint32(&h.fails, 1)
					continue
				}
				atomic.StoreUint32(&h.fails, 0)
			}
		}
	}()

}

func (h *Health) Stop() {
	h.ticker.Stop()
	close(h.stop)
}

func (h *Health) GetAddr() net.IP {
	return h.addr
}

func (h *Health) GetStatus() bool {
	return h.fails <= h.max_fails
}
