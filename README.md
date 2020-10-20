Caddy Module: http.handlers.cache
================================

**⚠️ Work-in-progress**

This is a distributed HTTP cache module for Caddy.

## Features

* Supports most HTTP cache headers defined in [RFC 7234](https://httpwg.org/specs/rfc7234.html) (see the TODO section for limitations)
* Sets [the `Cache-Status` HTTP Response Header](https://httpwg.org/http-extensions/draft-ietf-httpbis-cache-header.html)

## Example Configuration

See [`Caddyfile`](fixtures/Caddyfile) and [olricd.yaml](fixtures/olricd.yaml)

## TODO

We are looking for volunteers to improve this module. Are you interested? Please comment in an issue!

* [x] Add support for `Age`
* [ ] Add support for `Vary`
* [x] Enable the distributed mode of Olric (our cache library)
* [ ] Allow to serve stale responses if the backend is down or while the new version is being generated
* [x] Add Caddyfile directives
* [ ] Add support for the `stale-if-error` directive
* [ ] Add support for the `stale-while-revalidate` directive
* [ ] Add support for cache validation
* [ ] Add support for request coalescing
* [ ] Add support for cache invalidation (purge/ban)
* [ ] Add support for cache tags (similar to Varnish ykey)
* [ ] Add support for the `ttl` attribute of the `Cache-Status` header
