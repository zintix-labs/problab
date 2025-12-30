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

package server

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/server/api"
	"github.com/zintix-labs/problab/server/app"
	"github.com/zintix-labs/problab/server/netsvr"
	"github.com/zintix-labs/problab/server/svrcfg"
)

// Run 是 server 套件的「組裝器（assembler）」與「啟動入口（runtime entry）」。
//
// 它負責：
//  1. 驗證輸入的 SvrConfig（包含必要依賴，例如 logger）。
//  2. 建立 HTTP server（netsvr）。
//  3. 註冊路由與 middleware（api.RegisterRoutes）。
//  4. 啟動 app.Run() 並回傳停止原因。
//
// 注意：
//   - Run 不綁定任何「檔案路徑」或「環境變數」策略；所有依賴都應透過 SvrConfig 明確注入。
//   - 這裡提供預設啟動方式；若你要自訂 server 的組裝/路由/生命週期，
//     建議在你的專案內以 Problab 為核心自行組裝。
//     換句話說：server 套件不強迫你使用固定入口；你可以完全用自己的方式跑 server。
func Run(sCfg *svrcfg.SvrCfg) {
	if err := sCfg.Vaild(); err != nil {
		// 防止外層傳入的logger不可用
		fmt.Fprintln(os.Stderr, err)
		return
	}
	// Server
	svr := netsvr.NewChiServerDefault()

	// 註冊 Api
	api.RegisterRoutes(svr, sCfg)

	// 運行
	app := app.NewWith(svr)
	sCfg.Log.Info("[problab] listening on http://localhost" + svr.Address())
	if err := app.Run(); err != nil {
		sCfg.Log.Error("app stopped:", slog.Any("err", err))
	}
}

// RunWithSvr 與 Run() 相同，都是 server 套件提供的「組裝器（assembler）」與「啟動入口」。
//
// 差別在於：
//   - Run()：使用套件內建的預設 HTTP server（ChiAdapter）。這是最短路徑。
//   - RunWithSvr()：允許呼叫端注入自訂的 NetSvr（例如你自己包裝的 chi/gin/echo adapter、
//     自訂的 listener、額外的 server option、或你想把 server 生命週期接到既有框架）。
//
// 適用情境：
//   - 你希望沿用既有的 server / router / middleware 佈署方式。
//   - 你需要更細的 server 參數控制（Address、TLS、timeout、graceful shutdown 策略等）。
//   - 你希望把 Problab 的 API routes 掛載到你現有的服務中（作為子路由/子模組）。
//
// 重要行為與合約（contract）：
//   - RunWithSvr 會先做 SvrConfig 的基本驗證（包含 logger）。若驗證失敗，會額外把錯誤輸出到 stderr，
//     以避免上層「組裝失敗但無 log 可看」。
//   - svr 參數必須非 nil，且若是 ChiAdapter 會要求 Ready() 為 true（避免注入不完整的 server）。
//   - 這一層依然只負責「註冊 routes + 啟動 app.Run()」，不接管你整個系統的組裝方式。
//
// 進階控制：
//   - 若你需要完全掌握路由掛載方式、server 啟停、或把 Problab 與其他模組深度整合，
//     建議不要走 Run/RunWithSvr，而是直接在你的專案中持有並組裝 Problab（以及 SlotRuntime），
//     以你自己的方式建立 server 並呼叫 api.RegisterRoutes()。
func RunWithSvr(sCfg *svrcfg.SvrCfg, svr netsvr.NetSvr) {
	if err := sCfg.Vaild(); err != nil {
		// 防止外層傳入的logger不可用
		fmt.Fprintln(os.Stderr, err)
		return
	}
	if svr == nil {
		sCfg.Log.Error(errs.NewFatal("svr is required").Error())
		return
	} else {
		if s, ok := svr.(*netsvr.ChiAdapter); ok && !s.Ready() {
			sCfg.Log.Error(errs.NewFatal("default server is not ready").Error())
			return
		}
	}

	// 註冊 Api
	api.RegisterRoutes(svr, sCfg)

	// 運行
	app := app.NewWith(svr)
	sCfg.Log.Info("[problab] listening")
	if err := app.Run(); err != nil {
		sCfg.Log.Error("app stopped:", slog.Any("err", err))
	}
}
