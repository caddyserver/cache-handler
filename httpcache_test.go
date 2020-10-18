package httpcache_test

import (
	"testing"

	"github.com/caddyserver/caddy/v2/caddytest"
)

func TestMaxAge(t *testing.T) {
	tester := caddytest.NewTester(t)
	tester.InitServer(` 
	{
		http_port     9080
		https_port    9443
	}
	localhost:9080 {
		route /cache-max-age {
			cache
			header Cache-Control "max-age=60"
			respond "Hello, max-age!"
		}
	}`, "caddyfile")

	resp1, _ := tester.AssertGetResponse(`http://localhost:9080/cache-max-age`, 200, "Hello, max-age!")
	if resp1.Header.Get("Cache-Status") != "Caddy; fwd=uri-miss; stored" {
		t.Errorf("unexpected Cache-Status header %v", resp1.Header.Get("Cache-Status"))
	}

	resp2, _ := tester.AssertGetResponse(`http://localhost:9080/cache-max-age`, 200, "Hello, max-age!")
	if resp2.Header.Get("Cache-Status") != "Caddy; hit" {
		t.Errorf("unexpected Cache-Status header %v", resp2.Header.Get("Cache-Status"))
	}
}

func TestSMaxAge(t *testing.T) {
	tester := caddytest.NewTester(t)
	tester.InitServer(` 
	{
	  http_port     9080
	  https_port    9443
	}
	localhost:9080 {
		route /cache-s-maxage {
			cache
			header Cache-Control "s-maxage=5"
			respond "Hello, s-maxage!"
		}
	}`, "caddyfile")

	resp1, _ := tester.AssertGetResponse(`http://localhost:9080/cache-s-maxage`, 200, "Hello, s-maxage!")
	if resp1.Header.Get("Cache-Status") != "Caddy; fwd=uri-miss; stored" {
		t.Errorf("unexpected Cache-Status header %v", resp1.Header.Get("Cache-Status"))
	}

	resp2, _ := tester.AssertGetResponse(`http://localhost:9080/cache-s-maxage`, 200, "Hello, s-maxage!")
	if resp2.Header.Get("Cache-Status") != "Caddy; hit" {
		t.Errorf("unexpected Cache-Status header %v", resp2.Header.Get("Cache-Status"))
	}
}

func TestOlricConfig(t *testing.T) {
	tester := caddytest.NewTester(t)
	tester.InitServer(` 
	{
		http_port     9080
		https_port    9443
		cache {
			olric_config fixtures/olricd.yaml
		}
	}
	localhost:9080 {
		route /cache-max-age {
			cache
			header Cache-Control "max-age=60"
			respond "Hello, max-age!"
		}
	}`, "caddyfile")

	tester.AssertGetResponse(`http://localhost:9080/cache-max-age`, 200, "Hello, max-age!")
}
