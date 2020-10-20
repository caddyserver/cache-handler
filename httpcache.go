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

// config, db and dmap are global to preserve the cache between reloads and to share the same cache between all routes
var (
	cfg  *Config
	db   *olric.Olric
	dmap *olric.DMap
)

func init() {
	caddy.RegisterModule(Cache{})
	httpcaddyfile.RegisterGlobalOption("cache", parseCaddyfileGlobalOption)
	httpcaddyfile.RegisterHandlerDirective("cache", parseCaddyfileHandlerDirective)
}

type Config struct {
	// Path to the Olric's configuration file. See https://github.com/buraksezer/olric#client-server-mode.
	OlricConfig string `json:"olric_config,omitempty"`
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
		// the cache is immutable, provision it only one time
		return nil
	}

	if cfg == nil {
		// No global config (JSON format?), set the first handler config encountered as the global one
		cfg = &c.Config
	} else {
		// A global config is provided (caddyfile format?), always use it
		c.Config = *cfg
	}

	var olricConfig *config.Config
	if c.OlricConfig == "" {
		olricConfig = config.New("local")
		olricConfig.Cache.MaxInuse = int(512 << 20) // 512 MB
	} else {
		var err error
		if olricConfig, err = config.Load(c.OlricConfig); err != nil {
			return err
		}
	}

	started, cancel := context.WithCancel(context.Background())
	olricConfig.Started = func() {
		defer cancel()

		c.logger.Debug("olric is ready to accept connections")
	}
	var err error
	db, err = olric.New(olricConfig)
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

func (c *Cache) Validate() error {
	if cfg == nil || c.Config == *cfg {
		return nil
	}

	return fmt.Errorf("the cache configuration is global and immutable, it must have the exact same value for all handlers")
}

func (c *Cache) writeResponse(w http.ResponseWriter, rdr io.Reader, fromCache bool) error {
	// read the header and status first
	var meta metadata
	err := gob.NewDecoder(rdr).Decode(&meta)
	if err != nil {
		return err
	}

	header := w.Header()
	// set and write the cached headers
	for k, v := range meta.Header {
		header[k] = v
	}

	if fromCache {
		header.Set("Age", currentAge(meta.ResponseTime, meta.CorrectedInitialAge))
	}

	w.WriteHeader(meta.Status)

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

	return c.writeResponse(w, buf, true)
}

func (c *Cache) serveAndCache(ctx context.Context, key string, buf *bytes.Buffer) error {
	combo := ctx.Value(getterContextCtxKey).(getterContext)
	obj := cacheobject.Object{
		ReqDirectives: combo.reqDir,
		ReqHeaders:    combo.req.Header,
		ReqMethod:     combo.req.Method,
	}
	rv := cacheobject.ObjectResults{}

	var requestTime time.Time

	// we need to record the response if we are to cache it; only cache if
	// request is successful
	rr := caddyhttp.NewResponseRecorder(combo.rw, buf, func(status int, header http.Header) bool {
		var err error

		obj.RespStatusCode = status
		obj.RespHeaders = header

		obj.RespDirectives, err = cacheobject.ParseResponseCacheControl(obj.RespHeaders.Get("Cache-Control"))
		if err != nil {
			obj.RespHeaders.Add("Cache-Status", userAgent+"; fwd=request; detail=MALFORMED-CACHE-CONTROL")
			return false
		}

		if expires := obj.RespHeaders.Get("Expires"); expires != "" {
			if obj.RespExpiresHeader, err = http.ParseTime(expires); err != nil {
				obj.RespHeaders.Add("Cache-Status", userAgent+"; fwd=request; detail=MALFORMED-EXPIRES")
				return false
			}
		}

		if lastModified := obj.RespHeaders.Get("Last-Modified"); lastModified != "" {
			if obj.RespLastModifiedHeader, err = http.ParseTime(lastModified); err != nil {
				obj.RespHeaders.Add("Cache-Status", userAgent+"; fwd=request; detail=MALFORMED-LAST-MODIFIED")
				return false
			}
		}

		// Store the date as late as possible
		obj.NowUTC = time.Now().UTC() // response time
		date := obj.RespHeaders.Get("Date")
		if date == "" {
			// We need the Date header to be stored in the cache to compute the Age header
			obj.RespDateHeader = obj.NowUTC
			// A recipient with a clock that receives a response message without a Date header field
			// MUST record the time it was received and append a corresponding Date header field
			// to the message's header section if it is cached or forwarded downstream.
			// https://httpwg.org/specs/rfc7231.html#header.date
			obj.RespHeaders.Set("Date", obj.RespDateHeader.Format(http.TimeFormat))
		} else if obj.RespDateHeader, err = http.ParseTime(date); err != nil {
			obj.RespHeaders.Add("Cache-Status", userAgent+"; fwd=request; detail=MALFORMED-DATE")
			return false
		}

		cacheobject.CachableObject(&obj, &rv)
		if rv.OutErr != nil || len(rv.OutReasons) > 0 {
			obj.RespHeaders.Add("Cache-Status", fmt.Sprintf(userAgent+`; fwd=request; detail="%v"`, rv.OutReasons))
			return false
		}

		ageValue := obj.RespHeaders.Get("Age")
		correctedInitialAge := correctedInitialAge(obj.NowUTC, obj.RespDateHeader, requestTime, ageValue)

		if ageValue != "" && correctedInitialAge != 0 {
			// There is an intermediate cache, rewrite the header to include the network delay
			obj.RespHeaders.Set("Age", ageToString(correctedInitialAge))
		}

		// store metadata before the body, so we can efficiently
		// and conveniently use a single buffer for both; gob
		// decoder will only read up to end of gob message, and
		// the rest will be the body, which will be written
		// implicitly for us by the recorder
		err = gob.NewEncoder(buf).Encode(metadata{
			RequestTime:         requestTime,
			ResponseTime:        obj.NowUTC,
			CorrectedInitialAge: correctedInitialAge,

			Header: header,
			Status: status,
		})
		if err != nil {
			c.logger.Error("encoding headers for cache entry, not caching this request", zap.Error(err))
			return false
		}

		return true
	})

	requestTime = time.Now().UTC()
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

	ttl := rv.OutExpirationTime.Sub(obj.NowUTC)

	if ttl <= 0 {
		obj.RespHeaders.Add("Cache-Status", userAgent+"; fwd=uri-miss")
		return c.writeResponse(combo.rw, buf, false)
	}

	// add to cache
	if err := dmap.PutEx(key, buf.Bytes(), ttl); err != nil {
		return err
	}

	obj.RespHeaders.Add("Cache-Status", userAgent+"; fwd=uri-miss; stored")

	// Serve the response from bytes.Buffer
	return c.writeResponse(combo.rw, buf, false)
}

type metadata struct {
	RequestTime         time.Time
	ResponseTime        time.Time
	CorrectedInitialAge time.Duration
	Header              http.Header
	Status              int
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

func parseCaddyfileGlobalOption(d *caddyfile.Dispenser) (interface{}, error) {
	cfg = &Config{}
	for d.Next() {
		for d.NextBlock(0) {
			switch d.Val() {
			case "olric_config":
				if !d.NextArg() {
					return nil, d.ArgErr()
				}

				cfg.OlricConfig = d.Val()
			}
		}
	}

	return nil, nil
}

func parseCaddyfileHandlerDirective(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	c := &Cache{}
	if cfg != nil {
		c.Config = *cfg
	}

	return c, nil
}

// Interface guards
var (
	_ caddy.Provisioner           = (*Cache)(nil)
	_ caddyhttp.MiddlewareHandler = (*Cache)(nil)
	_ caddy.Validator             = (*Cache)(nil)
)
