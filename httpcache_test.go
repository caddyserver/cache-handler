package httpcache_test

import (
	"testing"

	"github.com/caddyserver/caddy/v2/caddytest"
)

func TestSMaxAge(t *testing.T) {
	tester := caddytest.NewTester(t)
	tester.InitServer(` 
	{
	  http_port     9080
	  https_port    9443
	}
	localhost:9080 {
		route /cache {
			cache
			header Cache-Control "s-maxage=5"
			respond "Hello, maxage!"
		}
	}`, "caddyfile")

	resp1, _ := tester.AssertGetResponse(`http://localhost:9080/cache`, 200, "Hello, maxage!")
	if resp1.Header.Get("Cache-Status") != "Caddy; fwd=uri-miss; stored" {
		t.Errorf("unexpected Cache-Status header %v", resp1.Header.Get("Cache-Status"))
	}

	resp2, _ := tester.AssertGetResponse(`http://localhost:9080/cache`, 200, "Hello, maxage!")
	if resp2.Header.Get("Cache-Status") != "Caddy; hit" {
		t.Errorf("unexpected Cache-Status header %v", resp2.Header.Get("Cache-Status"))
	}
}
