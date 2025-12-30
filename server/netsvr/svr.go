package netsvr

import (
	"net/http"

	"github.com/zintix-labs/problab/server/app"
)

// NetSvr 封裝「路由行為 + 服務啟停」的抽象介面。
//   - 只暴露給最外層 main 使用，其他層只需面向 NetRouter。
//   - 目的：依賴反轉。若改用不同 http 框架，只要實作此介面即可。
//   - 目前實作基於標準庫 net/http + chi 輕量路由，不支援 fasthttp/fiber 等非標準庫接口。
//     後續若要更換框架（例如 Gin、Echo），需提供相容 net/http handler 的實作。
//   - NetSvr 本身實作了 app.Component，因此可以直接交給 app.App 作為生命周期管理的一部分。
type NetSvr interface {
	NetRouter
	app.Component
}

// NetRouter 定義純路由行為，讓子模組只操作路由而不持有啟停控制權。
// Group 回呼只會拿到 NetRouter，看不到 Run/Shutdown，避免誤用。
// 此介面故意不包含 Run/Shutdown，方便在 handler / 子模組中注入、避免被誤用來控制 server 生命週期。
type NetRouter interface {
	// middleware
	Use(middleware func(http.Handler) http.Handler)

	// 註冊路由
	Get(path string, h http.HandlerFunc)
	Post(path string, h http.HandlerFunc)
	Put(path string, h http.HandlerFunc)
	Delete(path string, h http.HandlerFunc)

	// 群組路由
	Group(path string, fn func(NetRouter))
}
