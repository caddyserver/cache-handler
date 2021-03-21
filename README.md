Caddy Module: http.handlers.cache
================================

This is a distributed HTTP cache module for Caddy based on [Souin](https://github.com/darkweak/souin) cache.

## Features

* Supports most HTTP cache headers defined in [RFC 7234](https://httpwg.org/specs/rfc7234.html) (see the TODO section for limitations)
* Sets [the `Cache-Status` HTTP Response Header](https://httpwg.org/http-extensions/draft-ietf-httpbis-cache-header.html)
* Sets the `X-From-Cache` HTTP Response Header when it's served from the cache

## Example Configuration

See the [Souin](https://github.com/darkweak/souin) configuration for the full configuration, and his associated [Caddyfile](https://github.com/darkweak/souin/plugins/caddy/Caddyfile)  
See [`Caddyfile`](fixtures/Caddyfile) and [olricd.yaml](fixtures/olricd.yaml)
