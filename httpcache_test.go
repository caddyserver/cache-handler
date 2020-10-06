package httpcache_test

import (
	"testing"

	"github.com/caddyserver/caddy/v2/caddytest"
)

func TestDefaultTTL(t *testing.T) {
	tester := caddytest.NewTester(t)
	tester.InitServer(` 
	{
	  http_port     9080
	  https_port    9443
	}
	localhost:9080 {
		route /cache-default {
			cache {
				default_ttl 5
				olric_env local
			}
			respond "Hello, default!"
		}
	}`, "caddyfile")

	resp1, _ := tester.AssertGetResponse(`http://localhost:9080/cache-default`, 200, "Hello, default!")
	if resp1.Header.Get("Cache-Status") != "Caddy; fwd=uri-miss; stored" {
		t.Errorf("unexpected Cache-Status header %v", resp1.Header.Get("Cache-Status"))
	}

	resp2, _ := tester.AssertGetResponse(`http://localhost:9080/cache-default`, 200, "Hello, default!")
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
		route /cache-header {
			cache
			header Cache-Control "s-maxage=60"
			respond "Hello, Cache-Control header!"
		}
	}`, "caddyfile")

	resp1, _ := tester.AssertGetResponse(`http://localhost:9080/cache-header`, 200, "Hello, Cache-Control header!")
	if resp1.Header.Get("Cache-Status") != "Caddy; fwd=uri-miss; stored" {
		t.Errorf("unexpected Cache-Status header %v", resp1.Header.Get("Cache-Status"))
	}

	resp2, _ := tester.AssertGetResponse(`http://localhost:9080/cache-header`, 200, "Hello, Cache-Control header!")
	if resp2.Header.Get("Cache-Status") != "Caddy; hit" {
		t.Errorf("unexpected Cache-Status header %v", resp2.Header.Get("Cache-Status"))
	}
}
