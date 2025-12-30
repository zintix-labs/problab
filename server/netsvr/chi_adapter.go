// Copyright 2025 Zintix Labs
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

package netsvr

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

const defaultAddr string = ":5808"

// -----------------------------------------------------------------------------
//  Chi 服務
// -----------------------------------------------------------------------------

// ChiAdapter 以 chi (基於標準庫 net/http) 實作 NetSvr。
//   - 只使用標準庫介面：handler / middleware 都走 net/http，未支援 fasthttp/fiber 這類自訂協議。
//   - 若未來改用 Gin/Echo/自訂 server，可再寫新的 Adapter 實作 NetSvr。
type ChiAdapter struct {
	router chi.Router
	server *http.Server
	addr   string
}

// NewChiServer 建立自訂監聽位址的 ChiAdapter，含 http.Server 與預設 timeout。
func NewChiServer(addr string) *ChiAdapter {
	cr := chi.NewRouter()
	return &ChiAdapter{
		router: cr,
		server: &http.Server{
			Addr:         addr,
			Handler:      cr,
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  120 * time.Second,
		},
		addr: addr,
	}
}

// NewChiServerDefault 建立ChiAdapter
//
// 監聽port為3000
func NewChiServerDefault() *ChiAdapter {
	cr := chi.NewRouter()
	address := defaultAddr
	return &ChiAdapter{
		router: cr,
		server: &http.Server{
			Addr:         address,
			Handler:      cr,
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  120 * time.Second,
		},
		addr: address,
	}
}

// -----------------------------------------------------------------------------
//  介面實作 NetSvr / (會同時實作 Component)
// -----------------------------------------------------------------------------

func (c *ChiAdapter) Ready() bool {
	return (c != nil) && (c.router != nil) && (c.server != nil) &&
		(c.addr != "") && (strings.HasPrefix(c.addr, ":") || strings.Contains(c.addr, ":")) &&
		(c.server.Handler != nil) && (c.server.Handler == c.router)
}

func (c *ChiAdapter) Run() error {
	return c.server.ListenAndServe()
}

func (c *ChiAdapter) Shutdown(ctx context.Context) error {
	return c.server.Shutdown(ctx)
}

func (c *ChiAdapter) Use(mw func(http.Handler) http.Handler) {
	c.router.Use(mw)
}

func (c *ChiAdapter) Get(path string, h http.HandlerFunc) {
	c.router.Get(path, h)
}

func (c *ChiAdapter) Post(path string, h http.HandlerFunc) {
	c.router.Post(path, h)
}

func (c *ChiAdapter) Put(path string, h http.HandlerFunc) {
	c.router.Put(path, h)
}

func (c *ChiAdapter) Delete(path string, h http.HandlerFunc) {
	c.router.Delete(path, h)
}

func (c *ChiAdapter) Group(path string, fn func(subRouter NetRouter)) {
	c.router.Route(path, func(r chi.Router) {
		subAdapter := &ChiAdapter{
			router: r,
			server: nil,
		}
		fn(subAdapter)
	})
}

// -----------------------------------------------------------------------------
//  其他公開方法
// -----------------------------------------------------------------------------

func (c *ChiAdapter) Address() string {
	return c.addr
}
