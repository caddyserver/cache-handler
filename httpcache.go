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
	"sync"
	"time"

	"github.com/buraksezer/olric"
	"github.com/buraksezer/olric/config"
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/pquerna/cachecontrol/cacheobject"
	"go.uber.org/zap"
)

const userAgent = "Caddy"

func init() {
	caddy.RegisterModule(Cache{})
}

type Olric struct {
	Env           string `json:"env,omitempty"`
	BindPort      int    `json:"bind_port,omitempty"`
	DiscoveryPort int    `json:"discovery_port,omitempty"`
}

// Cache implements a simple distributed cache.
//
// Caches only GET and HEAD requests. Honors the Cache-Control: no-cache header.
//
// Still TODO:
//
// - Properly set autocache options
// - Eviction policies and API
// - Use single cache per-process
// - Preserve cache through config reloads
// - More control over what gets cached
type Cache struct {
	// Maximum size of the cache, in bytes. Default is 512 MB.
	MaxSize    int64 `json:"max_size,omitempty"`
	DefaultTTL int   `json:"default_ttl,omitempty"`
	Olric      Olric `json:"olric,omitempty"`

	db     *olric.Olric
	dmap   *olric.DMap
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

	maxSize := c.MaxSize
	if maxSize == 0 {
		const maxMB = 512
		maxSize = int64(maxMB << 20)
	}

	if c.Olric.Env == "" {
		c.Olric.Env = "local"
	}

	started, cancel := context.WithCancel(context.Background())
	cfg := config.New(c.Olric.Env)
	if c.Olric.BindPort != 0 {
		cfg.BindPort = c.Olric.BindPort
	}
	if c.Olric.DiscoveryPort != 0 {
		cfg.MemberlistConfig.BindPort = c.Olric.DiscoveryPort
		cfg.MemberlistConfig.AdvertisePort = c.Olric.DiscoveryPort
	}
	cfg.Cache.MaxInuse = int(maxSize)
	cfg.Started = func() {
		defer cancel()

		c.logger.Info("olric is ready to accept connections")
	}
	var err error
	c.db, err = olric.New(cfg)
	if err != nil {
		return err
	}

	errCh := make(chan error, 1)
	go func() {
		err = c.db.Start()
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
	c.dmap, err = c.db.NewDMap(dmapName)
	return err
}

// Cleanup shutdowns Olric's DB.
func (c *Cache) Cleanup() error {
	if c.db == nil {
		return nil
	}

	return c.db.Shutdown(context.Background())
}

// Validate validates c.
func (c *Cache) Validate() error {
	if c.MaxSize < 0 {
		return fmt.Errorf("size must be greater than 0")
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
	case "GET":
	case "HEAD":
	case "POST":
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
	key := r.RequestURI

	// the buffer will store the gob-encoded header, then the body
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	value, err := c.dmap.Get(key)
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
	if err := c.dmap.PutEx(key, buf.Bytes(), ttl); err != nil {
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

// Interface guards
var (
	_ caddy.Provisioner           = (*Cache)(nil)
	_ caddy.CleanerUpper          = (*Cache)(nil)
	_ caddy.Validator             = (*Cache)(nil)
	_ caddyhttp.MiddlewareHandler = (*Cache)(nil)
)
