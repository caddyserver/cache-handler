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
	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/darkweak/souin/cache/coalescing"
	"github.com/darkweak/souin/plugins"
	souinCaddy "github.com/darkweak/souin/plugins/caddy"
	"go.uber.org/zap"
	"net/http"
)

func init() {
	caddy.RegisterModule(Cache{})
	httpcaddyfile.RegisterGlobalOption("cache", parseCaddyfileGlobalOption)
	httpcaddyfile.RegisterHandlerDirective("cache", parseCaddyfileHandlerDirective)
}

var staticConfig souinCaddy.Configuration

// Cache declaration.
type Cache struct {
	plugins.SouinBasePlugin
	configuration     *souinCaddy.Configuration
	logger            *zap.Logger
}

// CaddyModule returns the Caddy module information.
func (s Cache) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.cache",
		New: func() caddy.Module { return new(Cache) },
	}
}

// ServeHTTP implements caddyhttp.MiddlewareHandler.
func (s Cache) ServeHTTP(rw http.ResponseWriter, req *http.Request, next caddyhttp.Handler) error {
	coalescing.ServeResponse(rw, req, s.Retriever, plugins.DefaultSouinPluginCallback, s.RequestCoalescing)
	return next.ServeHTTP(rw, req)
}

// Validate to validate configuration
func (s *Cache) Validate() error {
	s.logger.Info("Keep in mind the existing keys are always stored with the previous configuration. Use the API to purge existing keys")
	return nil
}

// Provision to do the provisioning part
func (s *Cache) Provision(ctx caddy.Context) error {
	s.logger = ctx.Logger(s)
	s.RequestCoalescing = coalescing.Initialize()
	if s.configuration == nil && &staticConfig != nil {
		s.configuration = &staticConfig
	}
	s.Retriever = plugins.DefaultSouinPluginInitializerFromConfiguration(s.configuration)
	return nil
}

func parseCaddyfileGlobalOption(d *caddyfile.Dispenser) (interface{}, error) {
	p := NewParser()

	for d.Next() {
		for nesting := d.Nesting(); d.NextBlock(nesting); {
			v := d.Val()
			d.NextArg()
			v2 := d.Val()

			p.WriteLine(v, v2)
		}
	}

	var s Cache
	err := staticConfig.Parse([]byte(p.str))
	s.configuration = &staticConfig
	return nil, err
}

func parseCaddyfileHandlerDirective(_ httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	s := &Cache{}
	if &staticConfig != nil {
		s.configuration = &staticConfig
	}

	return s, nil
}

// Interface guards
var (
	_ caddy.Provisioner           = (*Cache)(nil)
	_ caddyhttp.MiddlewareHandler = (*Cache)(nil)
	_ caddy.Validator             = (*Cache)(nil)
)
