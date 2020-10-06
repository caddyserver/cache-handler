// Copyright 2015 Matthew Holt and The Caddy Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package httpcache

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/buraksezer/olric"
	"github.com/buraksezer/olric/config"
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/pquerna/cachecontrol/cacheobject"
	"go.uber.org/zap"
)

const userAgent = "Caddy"

// db and dmap are global to preserve the cache between reloads and to share the same cache between all routes
// These variables are destroyed and recreated if and only if there is a configuration change
var (
	db             *olric.Olric
	dmap           *olric.DMap
	previousConfig Config
)

func init() {
	caddy.RegisterModule(Cache{})
	httpcaddyfile.RegisterHandlerDirective("cache", parseCaddyfile)
}

type Config struct {
	// Maximum size of the cache, in bytes. Default is 512 MB.
	MaxSize int64 `json:"max_size,omitempty"`
	// Default Time To Live for responses with no Cache-Control HTTP headers, in seconds. Default is 0.
	DefaultTTL int `json:"default_ttl,omitempty"`
	// Configuration of the Olric cache data store.
	Olric Olric `json:"olric,omitempty"`
}

type Olric struct {
	// The network environment: local, lan or wan. See https://pkg.go.dev/github.com/buraksezer/olric/config#New for details.
	Env string `json:"env,omitempty"`
	// TODO: we'll likely need more options here
}

// Cache implements a simple distributed cache.
//
// Caches only GET and HEAD requests. Honors the Cache-Control: no-cache header.
//
// Still TODO:
//
// - Properly set olric options
// - Eviction policies and API
// - Use single cache per-process
type Cache struct {
	Config
	logger *zap.Logger
}

// CaddyModule returns the Caddy module information.
func (Cache) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.cache",
		New: func() caddy.Module { return new(Cache) },
	}
}

// Provision provisions c.
func (c *Cache) Provision(ctx caddy.Context) error {
	c.logger = ctx.Logger(c)

	if db != nil {
		// the cache is immutable
		return nil
	}

	previousConfig = c.Config

	maxSize := c.MaxSize
	if maxSize == 0 {
		const maxMB = 512
		maxSize = int64(maxMB << 20)
	}

	env := c.Olric.Env
	if env == "" {
		env = "local"
	}

	started, cancel := context.WithCancel(context.Background())
	cfg := config.New(env)
	cfg.Cache.MaxInuse = int(maxSize)
	cfg.Started = func() {
		defer cancel()

		c.logger.Debug("olric is ready to accept connections")
	}
	var err error
	db, err = olric.New(cfg)
	if err != nil {
		return err
	}

	errCh := make(chan error, 1)
	go func() {
		// This isn't necessary to shutdown Olric explicitly during Caddy's shutdown because Olric handles SIGTERM by itself
		err = db.Start()
		if err != nil {
			c.logger.Error("olric.Start returned an error", zap.Error(err))
			errCh <- err
		}
	}()
	select {
	case err = <-errCh:
		return err
	case <-started.Done():
	}
	dmap, err = db.NewDMap(dmapName)
	return err
}

// Validate validates c.
func (c *Cache) Validate() error {
	if (c.Config != previousConfig && c.Config != Config{}) {
		// TODO(dunglas): be smarter than that and whitelist some config keys such as DefaultTTL
		return fmt.Errorf("the configuration of the cache cannot be changed without restarting the server(s)")
	}

	if c.MaxSize < 0 {
		return fmt.Errorf("size must be greater than 0")
	}
	if c.Olric.Env != "" && c.Olric.Env != "local" && c.Olric.Env != "lan" && c.Olric.Env != "wan" {
		return fmt.Errorf("available environments are local, lan and wan")
	}

	return nil
}

func (c *Cache) writeResponse(w http.ResponseWriter, rdr io.Reader) error {
	// read the header and status first
	var hs headerAndStatus
	err := gob.NewDecoder(rdr).Decode(&hs)
	if err != nil {
		return err
	}

	// set and write the cached headers
	for k, v := range hs.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(hs.Status)

	// write the cached response body
	_, err = io.Copy(w, rdr)
	return err
}

func (c *Cache) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	// TODO(dunglas): use functions added in https://github.com/pquerna/cachecontrol/pull/18 if merged
	switch r.Method {
	case http.MethodGet:
	case http.MethodHead:
	case http.MethodPost:
	default:
		// method not cacheable
		w.Header().Add("Cache-Status", userAgent+"; fwd=request; detail=METHOD")
		return next.ServeHTTP(w, r)
	}

	reqDir, err := cacheobject.ParseRequestCacheControl(r.Header.Get("Cache-Control"))
	if err != nil || reqDir.NoCache || reqDir.NoStore {
		// TODO: implement no-cache properly (add support for validation)
		w.Header().Add("Cache-Status", userAgent+"; fwd=request; detail=DIRECTIVE")
		return next.ServeHTTP(w, r)
	}

	getterCtx := getterContext{w, r, next, reqDir}
	ctx := context.WithValue(r.Context(), getterContextCtxKey, getterCtx)
	// TODO: add support for the Vary header
	key := strings.Join([]string{r.Host, r.RequestURI, r.Method}, "-")

	// the buffer will store the gob-encoded header, then the body
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	value, err := dmap.Get(key)
	if err != nil {
		if err == olric.ErrKeyNotFound {
			// Cache the request here
			return c.serveAndCache(ctx, key, buf)
		}
		return err
	}

	// We found the key in the Olric cluster.
	buf.Write(value.([]byte))
	w.Header().Add("Cache-Status", userAgent+"; hit")

	return c.writeResponse(w, buf)
}

func (c *Cache) serveAndCache(ctx context.Context, key string, buf *bytes.Buffer) error {
	combo := ctx.Value(getterContextCtxKey).(getterContext)
	respHeaders := combo.rw.Header()
	obj := cacheobject.Object{
		ReqDirectives: combo.reqDir,
		ReqHeaders:    combo.req.Header,
		ReqMethod:     combo.req.Method,

		NowUTC: time.Now().UTC(),
	}
	rv := cacheobject.ObjectResults{}

	// we need to record the response if we are to cache it; only cache if
	// request is successful
	rr := caddyhttp.NewResponseRecorder(combo.rw, buf, func(status int, header http.Header) bool {
		var err error
		resDir, err := cacheobject.ParseResponseCacheControl(respHeaders.Get("Cache-Control"))
		if err != nil {
			respHeaders.Add("Cache-Status", userAgent+"; fwd=request; detail=MALFORMED-CACHE-CONTROL")
			return false
		}

		if expires := respHeaders.Get("Expires"); expires != "" {
			if obj.RespExpiresHeader, err = http.ParseTime(expires); err != nil {
				respHeaders.Add("Cache-Status", userAgent+"; fwd=request; detail=MALFORMED-EXPIRES")
				return false
			}
		}

		if date := respHeaders.Get("Date"); date != "" {
			if obj.RespDateHeader, err = http.ParseTime(date); err != nil {
				respHeaders.Add("Cache-Status", userAgent+"; fwd=request; detail=MALFORMED-DATE")
				return false
			}
		}

		if lastModified := respHeaders.Get("Last-Modified"); lastModified != "" {
			if obj.RespLastModifiedHeader, err = http.ParseTime(lastModified); err != nil {
				respHeaders.Add("Cache-Status", userAgent+"; fwd=request; detail=MALFORMED-LAST-MODIFIED")
				return false
			}
		}

		obj.RespDirectives = resDir
		obj.RespHeaders = respHeaders
		obj.RespStatusCode = status

		cacheobject.CachableObject(&obj, &rv)
		if rv.OutErr != nil || len(rv.OutReasons) > 0 {
			respHeaders.Add("Cache-Status", fmt.Sprintf(userAgent+`; fwd=request; detail="%v"`, rv.OutReasons))
			return false
		}

		// store the header before the body, so we can efficiently
		// and conveniently use a single buffer for both; gob
		// decoder will only read up to end of gob message, and
		// the rest will be the body, which will be written
		// implicitly for us by the recorder
		err = gob.NewEncoder(buf).Encode(headerAndStatus{
			Header: header,
			Status: status,
		})
		if err != nil {
			c.logger.Error("encoding headers for cache entry, not caching this request", zap.Error(err))
			return false
		}

		return true
	})

	// execute next handlers in chain
	err := combo.next.ServeHTTP(rr, combo.req)
	if err != nil {
		return err
	}

	// if response body was not buffered, response was
	// already written and we are unable to cache; or,
	// if there was no response to buffer, same thing.
	// TODO: maybe Buffered() should return false if there was no response to buffer (which would account for the case when shouldBuffer is never called)
	if !rr.Buffered() || buf.Len() == 0 {
		return errUncacheable
	}

	cacheobject.ExpirationObject(&obj, &rv)

	var zeroTime time.Time
	var ttl time.Duration
	if rv.OutExpirationTime == zeroTime {
		ttl = time.Duration(c.DefaultTTL) * time.Second
	} else {
		ttl = rv.OutExpirationTime.Sub(obj.NowUTC)
	}

	if ttl <= 0 {
		respHeaders.Add("Cache-Status", userAgent+"; fwd=uri-miss")
		return c.writeResponse(combo.rw, buf)
	}

	// add to cache
	if err := dmap.PutEx(key, buf.Bytes(), ttl); err != nil {
		return err
	}

	respHeaders.Add("Cache-Status", userAgent+"; fwd=uri-miss; stored")

	// Serve the response from bytes.Buffer
	return c.writeResponse(combo.rw, buf)
}

type headerAndStatus struct {
	Header http.Header
	Status int
}

type getterContext struct {
	rw     http.ResponseWriter
	req    *http.Request
	next   caddyhttp.Handler
	reqDir *cacheobject.RequestCacheDirectives
}

var bufPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

var errUncacheable = fmt.Errorf("uncacheable")

const dmapName = "http_requests"

type ctxKey string

const getterContextCtxKey ctxKey = "getter_context"

// UnmarshalCaddyfile sets up the handler from Caddyfile tokens. Syntax:
//
//     cache {
//         default_ttl <ttl>
//         max_size <size>
//         olric_env <env>
//     }
func (c *Cache) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		for d.NextBlock(0) {
			switch d.Val() {
			case "max_size":
				if !d.NextArg() {
					return d.ArgErr()
				}

				maxSize, err := strconv.ParseInt(d.Val(), 10, 64)
				if err != nil {
					return d.Errf("bad max_size value '%s': %v", d.Val(), err)
				}

				c.MaxSize = maxSize

			case "default_ttl":
				if !d.NextArg() {
					return d.ArgErr()
				}

				defaultTTL, err := strconv.Atoi(d.Val())
				if err != nil {
					return d.Errf("bad default_ttl value '%s': %v", d.Val(), err)
				}

				c.DefaultTTL = defaultTTL

			case "olric_env":
				if !d.NextArg() {
					return d.ArgErr()
				}

				c.Olric.Env = d.Val()
			}
		}
	}
	return nil
}

// parseCaddyfile unmarshals tokens from h into a new Middleware.
func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var c Cache
	err := c.UnmarshalCaddyfile(h.Dispenser)
	return &c, err
}

// Interface guards
var (
	_ caddy.Provisioner           = (*Cache)(nil)
	_ caddy.Validator             = (*Cache)(nil)
	_ caddyhttp.MiddlewareHandler = (*Cache)(nil)
	_ caddyfile.Unmarshaler       = (*Cache)(nil)
)
