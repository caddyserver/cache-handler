Caddy Module: http.handlers.cache
================================

**⚠️ Work-in-progress**

This is a distributed HTTP cache module for Caddy.

## Features

* Supports most HTTP cache headers defined in [RFC 7234](https://httpwg.org/specs/rfc7234.html) (see the TODO section for limitations)
* Sets [the `Cache-Status` HTTP Response Header](https://httpwg.org/http-extensions/draft-ietf-httpbis-cache-header.html)

## Example Configuration

See [`caddy_cache.json`](caddy_cache.json)

## TODO

We are looking for volunteers to improve this module. Are you interested? Please comment in an issue!

* [ ] Add support for `Vary`
* [ ] Enable the distributed mode of Olric (our cache library)
* [ ] Add Caddyfile directives
* [ ] Add support for cache validation
* [ ] Add support for cache invalidation (purge/ban)
* [ ] Add support for cache tags (similar to Varnish ykey)
