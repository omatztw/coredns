package failover

import (
	"net"
	"testing"
	"time"

	"github.com/coredns/caddy"
	"github.com/stretchr/testify/assert"
)

func TestFailoverParse(t *testing.T) {
	tests := []struct {
		inputRules string
		shouldErr  bool
		expectedHc []Health
	}{
		{
			`failover {
				192.168.1.10 http://192.168.1.7:8080/ 5s 200 0
				192.168.1.11 http://192.168.1.7:8081/ 5s 200 0
				192.168.1.12 http://192.168.1.7:8082/ 5s 200 0
				192.168.1.13 http://192.168.1.7:8083/ 5s 200 0
				192.168.1.14 http://192.168.1.7:8084/ 5s 200 0
				192.168.1.15 http://192.168.1.7:8085/ 5s 200 0
				192.168.1.16 http://192.168.1.7:8086/ 5s 200 0
			}`,
			false, []Health{
				{addr: net.ParseIP("192.168.1.10"), ep: "http://192.168.1.7:8080/", interval: 5 * time.Second, statusCode: 200, max_fails: 0},
				{addr: net.ParseIP("192.168.1.11"), ep: "http://192.168.1.7:8081/", interval: 5 * time.Second, statusCode: 200, max_fails: 0},
				{addr: net.ParseIP("192.168.1.12"), ep: "http://192.168.1.7:8082/", interval: 5 * time.Second, statusCode: 200, max_fails: 0},
				{addr: net.ParseIP("192.168.1.13"), ep: "http://192.168.1.7:8083/", interval: 5 * time.Second, statusCode: 200, max_fails: 0},
				{addr: net.ParseIP("192.168.1.14"), ep: "http://192.168.1.7:8084/", interval: 5 * time.Second, statusCode: 200, max_fails: 0},
				{addr: net.ParseIP("192.168.1.15"), ep: "http://192.168.1.7:8085/", interval: 5 * time.Second, statusCode: 200, max_fails: 0},
				{addr: net.ParseIP("192.168.1.16"), ep: "http://192.168.1.7:8086/", interval: 5 * time.Second, statusCode: 200, max_fails: 0},
			},
		},
		{
			`failover {
				192.168.1.10 http://192.168.1.7:8080/ dummy 200 0
			}`,
			true, []Health{},
		},
	}

	for _, test := range tests {
		c := caddy.NewTestController("dns", test.inputRules)
		fo, err := failoverParse(c)
		if test.shouldErr {
			assert.Error(t, err)
		} else if !test.shouldErr {
			assert.NoError(t, err)
			for j, hc := range fo.HealthChecks {
				assert.Equal(t, test.expectedHc[j].addr, hc.addr, "")
				assert.Equal(t, test.expectedHc[j].ep, hc.ep, "")
				assert.Equal(t, test.expectedHc[j].interval, hc.interval, "")
				assert.Equal(t, test.expectedHc[j].max_fails, hc.max_fails, "")
				assert.Equal(t, test.expectedHc[j].statusCode, hc.statusCode, "")
			}
		}
	}
}
