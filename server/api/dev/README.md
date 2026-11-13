# ProbLab Dev

用瀏覽器逐段、逐步看一局的完整流程：盤面、中獎位置、每一步的贏分、選擇類遊戲的每一段，以及每台自己的特色（標記、進度條、角標等）。用途是開發期的除錯與重現，不做統計；統計請用 `make run` 或 `/v1/sim`。

> dev 路由只在 `ModeDev` 與 `ModePanel` 註冊，沒有任何驗證機制，**不得對外公開**。

## 啟動

```sh
make dev   # 等於 go run ./cmd/dev，自動開啟 http://localhost:5808/dev
go run ./cmd/dev -addr :5810 -open=false   # 換埠號、不開瀏覽器（例如同時開兩個）
```

Ctrl+C 結束；仍有連線時最多等 5 秒。

`make dev` 以 `svrcfg.ModePanel` 啟動：只掛首頁與 `/dev`，沒有 `/v1`，也不建立機台池；engine 以 `problab.New` 加 `RegisterAll`、`Freeze` 組裝，所以每台的 optimal 資料在第一次 Spin 才載入，不會在啟動時一次載入全部。要同時使用 `/v1/*`，請用 `make svr`（`ModeDev`）。

公版前端以 `go:embed` 包在 `server/api/dev/web/`，改了之後要重新啟動。

鍵盤：Space／Enter 執行 Spin，←／→ 切換上一步／下一步，I 切換「顯示」。格子編號平常不顯示，滑鼠移到格子上，或點一下釘住（再點一次取消）才顯示。

盤面下方的「顯示」切換格子與「本步中獎」的圖標欄要寫名稱（`H1`）還是盤面上的 ID（該 mode `symbols` 的索引，也就是後端陣列裡的數字）；ID 模式下空格與不存在的格子也寫出原始值，方便逐格比對。滑鼠移到格子上一律同時顯示兩者，瀏覽器會記住上次的選擇。下方時間軸依「段 > Mode > Round」分組列出每個 act，點一下就跳過去；只有一段的局不顯示段的層級。

## 頂部欄

| 欄位 | 說明 |
| --- | --- |
| 機台、押注模式 | 由 `/dev/meta` 載入 |
| 倍數 | 投注倍數 1～100，預設 1；旁邊顯示投注額（押注單位 × 倍數）。贏分由遊戲邏輯乘上倍數，續段與回放沿用同一個倍數 |
| 選擇方式 | 選擇類遊戲遇到選擇點時的處理方式：`first`（預設，選位置 0）、`last`（選最後一個位置）、`player`（停在選擇點，由你選） |
| 輸入 | `Seed` 或 `Snap`，明確指定，後端不從內容判斷 |
| 值 | Seed：留空自動產生；十進位整數（int64）；`hex:…`；`utf8:…`。不帶前綴的其他字串一律拒絕。Snap：某一段的 `start_b64u` |
| 局數 | Seed 時 1～1000，超過截為 1000；Snap 與 player 固定 1 |

### Seed 與 Snap

| | Seed | Snap |
| --- | --- | --- |
| 是什麼 | 建立機台的根 seed material | 某一段實際使用的 RNG 起點（結果中的 `start_b64u`） |
| 重現保證 | 同一組（機台、押注模式、倍數、選擇方式、seed、局數）結果完全相同，包含 optimal 與選擇類遊戲 | 只保證第 0 段；之後的段依選擇方式繼續，不一定與原局相同，畫面會標示 |

Seed 留空時，狀態列會顯示實際使用的 seed，可以原樣貼回重現。

## 回放

| 方式 | 做法 | 範圍 |
| --- | --- | --- |
| 回放這局 | 右側 DTO 面板的按鈕：以這局每一段記錄的 `start_b64u`，加上續段的 `choice`、`choose_next` 與 `cp`，逐段重跑並比對 | 整局完全重現，包含 optimal 依入口抽庫的續段 |
| 用這段快照重跑 | 第 0 段的按鈕：輸入切到 Snap、填入 `start_b64u` 後執行 | 只保證第 0 段 |
| 手動貼 Snap | 從正式後端的紀錄貼上某一局第 0 段的 `start_b64u` | 只保證第 0 段 |

一局有多段時，單一快照無法還原續段的輸入，所以要完整重現必須用「回放這局」。

## player 模式

選擇方式選 `player` 後按 Spin，這局停在選擇點時，時間軸末端會列出選項（`#位置 → 入口值`）。按下後送出一個續段請求，新的段接在同一局後面；局結束後左側總計才計入這一局。續段的格式與正式環境相同，伺服器不保存狀態；續段的起點依機台的 RNG 政策取得，每一段都會記下自己的 `start_b64u`，事後一樣可以「回放這局」。

## 網址參數

```
http://localhost:5808/dev?gid=2&betmode=0&betmult=1&choose=first&input=seed&value=5&rounds=300&spin=1&bet=137&step=6
```

`spin=1` 時載入後立即執行，再跳到第 `bet` 局的第 `step` 步（都從 1 起算）。同一組參數一定開出同一個畫面（Snap 輸入只保證第 0 段）。加上 `symmode=id` 會以 ID 模式開啟。

## 限制

| 項目 | 限制 |
| --- | --- |
| 每次批次的局數 | 1～1000 |
| 投注倍數 | 1～100 |
| 每局的段數 | `problab.DefaultMaxSegments` |
| 請求內容大小 | 1 MB |

一批的結果整批算完才回傳，只放在瀏覽器記憶體中，不寫入瀏覽器儲存；瀏覽器只記住上次選的機台與「顯示」的選擇。

## API

| 方法與路徑 | 用途 |
| --- | --- |
| `GET /dev/meta` | 機台清單：gid、名稱、logic、押注單位、最大贏分、`fixed`、各 mode 的行列數與圖標名稱、是否注入外掛（`plugin`）；另有 `max_rounds`、`max_segments`、`max_bet_mult`，以及是否注入主題（`theme`）。不輸出盤面產生、賠率與中獎判定的設定 |
| `POST /dev/spin` | `{gid, betmode, betmult, choose, input, value, rounds}` → `{seed, betmult, rounds, clamped, partial_replay, total_bet, total_win, rtp, bets}`；每一局是 `{index, bet, win, isend, segments}`，`segments` 是每一段的 SpinResult |
| `POST /dev/next` | player 的續段：`{gid, betmode, betmult, cycle, choice, choose_next, cp}` → `{segment}` |
| `POST /dev/replay` | `{gid, betmode, betmult, segments: [{start_b64u, cycle, choice, choose_next, cp}]}` → `{bet}` |

- `betmult` 省略時為 1；續段與回放必須和第 0 段相同。一局的投注額是押注單位 × 倍數。
- 一局的贏分是各段贏分的加總；投注只計第 0 段一次。`total_bet`、`total_win`、`rtp` 只計已結束的局。
- `choose`、`input` 必填；未知欄位、格式錯誤一律回 400，錯誤內容是純文字。
- 盤面 `screen` 是列優先：`idx = row × columns + col`；圖標 ID 是該 mode `symbols` 的索引。

## 機台外掛

每台可以有一支 `<gid>.js`，負責畫這台特有的東西。problab 不內建任何機台的外掛：外掛一律由應用端以 `svrcfg.SvrCfg.DevGames` 注入，沒有注入的機台只顯示公版（盤面、中獎、時間軸與 DTO 照常顯示，ext 只以原始 JSON 呈現）。demo 0～3 的外掛在 `demo/demo_devgames/`，由 `cmd/dev` 注入，可以當範例。在自己的專案注入外掛的方式見下方「在自己的專案使用」。

```js
DEV.register(2, {
  title: "demo_choice 資訊",           // 右側面板標題
  css: ".cell.blast { outline-color: red; }", // 這台專用的樣式，選填
  panel(ctx) { return "<html>"; },     // 右側面板內容
  cellClass(idx, ctx) { return []; },  // 格子要加的 class，例如 "mark"、"dim" 或自訂的
  badge(idx, ctx) { return ""; },      // 格子右下角的小字
  actLabel(act, ctx) { return act.acttype; }, // 時間軸上每一步的文字
});
```

每個欄位都可以省略。`ctx` 有：

| 欄位 | 內容 |
| --- | --- |
| `game` | `/dev/meta` 的這台資料（含 `fixed`、`modes`） |
| `bet` | 這一局：`{index, bet, win, isend, segments}` |
| `segment`、`segIdx` | 目前這一步所在的段（SpinResult）與段號 |
| `mode` | 目前的 game mode（`modeid`、`trigger`、`win`、`acts`） |
| `act` | 目前這一步（`acttype`、`round`、`step`、各種贏分、`screen`、`details`、`ext`） |
| `ext`、`prevExt` | 這一步和上一步的 ext；這一步沒有 ext 時，沿用這局最近一次出現的值 |
| `screen`、`prevScreen`、`prevAct` | 這一步和上一步的盤面與 act；沒有盤面的步驟沿用同一個 mode 的前一個盤面 |
| `modeInfo`、`symbols` | 這個 mode 的行列數和圖標名稱 |

工具函式：`DEV.util.kv(rows)`（兩欄表）、`DEV.util.progress(value, total, label)`（進度條）、`DEV.util.sym(ctx, id)`（圖標 ID 轉名稱）、`DEV.util.esc(s)`。

## 自訂配色

面板的顏色都是 `app.css` 的 CSS 變數。應用端可以注入一支 `theme.css` 換掉預設值，也可以只改某一台或某一個圖標；沒有覆寫的變數維持預設。

### 預設配色

| 圖標 | 樣式 |
| --- | --- |
| H | 依編號用色序當實心底色 |
| L | 依編號用色序混入卡片底色當淡底，字色是加深的色序 |
| W | 灰白底、金框、深金字 |
| C | 白底、青框、青字 |
| S | 鵝黃白底、石板灰框、深灰字 |
| Z（空格） | 不寫名稱、不填底色，只留很淡的框 |
| 其他 | 固定的灰色 |
| 負值（Trim 裁切掉的 -1、遊戲封鎖的格子） | 不存在的格子：不畫底色也不畫框，只標一個淡淡的 ×，盤面輪廓就是實際形狀；滑鼠提示顯示原始值 |

H、L 的色序 1→6 為紫、橘、黃、藍、紅、綠，7 以後從 1 重來但變淡。特殊圖標（W、C、S）都有 2px 外框，中獎時外框照樣保留。

### 可以覆寫的變數

| 變數 | 用途 | 淺色預設 | 深色預設 |
| --- | --- | --- | --- |
| `--tone-1`～`--tone-6` | H、L 的色序 | `#8b5cf6`、`#f97316`、`#eab308`、`#3b82f6`、`#ef4444`、`#22c55e` | 同左 |
| `--tone-1-text`～`--tone-6-text` | H 的字色 | `--tone-3-text` 為 `#1d2329`，其餘 `#fff` | 同左 |
| `--sym-l-bg` | L 的淡底中色序所佔的比例，其餘是卡片底色 | `16%` | `22%` |
| `--sym-l-text` | L 的字色中色序所佔的比例，其餘是 `--sym-l-ink` | `75%` | `80%` |
| `--sym-l-ink` | L 的字色混入的顏色 | `#000` | `#fff` |
| `--sym-w`、`--sym-w-text`、`--sym-w-ring` | W 的底色、字色、外框 | `#f5f5f4`、`#a16207`、`#ca8a04` | `#e7e5e4`、`#92400e`、`#ca8a04` |
| `--sym-c`、`--sym-c-text`、`--sym-c-ring` | C 的底色、字色、外框 | `#ffffff`、`#0e7490`、`#0891b2` | `#ffffff`、`#155e75`、`#0e7490` |
| `--sym-s`、`--sym-s-text`、`--sym-s-ring` | S 的底色、字色、外框 | `#fff6d5`、`#334155`、`#475569` | `#fff6d5`、`#1e293b`、`#334155` |
| `--sym-x` | 其他圖標的底色 | `#d4d4d4` | `#404040` |
| `--hit`、`--hit-glow` | 得分框與光暈 | `#10b981`、`rgba(16, 185, 129, .50)` | `#34d399`、同左 |
| `--mark` | 外掛加上 `mark` class 時的外框 | `#db2777` | 同左 |
| `--primary` | 主題色（按鈕、選取） | `#059669` | `#047857` |

其他變數（底色、邊框、字色等）見 `app.css` 的 `:root`。

### 注入 theme.css

1. 在外掛目錄（`DevGames` 指向的目錄，例如下方的 `internal/devgames/`）新增 `theme.css`，只寫要改的變數。只想改色、沒有外掛的專案，目錄裡只放 `theme.css` 也可以。
2. `embed.go` 的 pattern 要包含 css：`//go:embed *.js *.css`。目錄裡只有 `theme.css` 時寫 `//go:embed theme.css`。
3. 重新啟動 dev 入口。`/dev/meta` 的 `theme` 會變成 `true`，面板自動在 `app.css` 之後載入 `/dev/games/theme.css`。
4. 調色時把 `devGames` 換成 `os.DirFS("internal/devgames")`，存檔後重新整理瀏覽器就看得到，不用重新啟動。

`DevGames` 裡只有 `<gid>.js` 與 `theme.css` 會送出，其他檔名一律 404。

### 覆寫的範圍

| 範圍 | 寫在哪裡 | 寫法 |
| --- | --- | --- |
| 整個面板 | `theme.css` | `:root { … }` |
| 某一台 | `theme.css`，或該台外掛的 `css` | `body[data-gid="<gid>"] { … }` |
| 某一個圖標 | 同上 | `.cell[data-sym="<圖標名稱>"] { … }`；H、L 改 `--tone-base`（H 的字色是 `--tone-text`），W、C、S 改 `--sym-w`、`--sym-c-ring` 這類變數 |

樣式的順序是 `app.css` → `theme.css` → 外掛的 `css`，後面的蓋前面的。外掛的樣式在切換機台後仍留在頁面上，所以外掛裡的規則一律要以 `body[data-gid="<gid>"]` 限定。

深色的預設值寫在 `@media (prefers-color-scheme: dark)` 裡。`theme.css` 只寫 `:root` 時，深色也會套用同一個值；深色要用不同的值，就在 `theme.css` 裡另外寫一段 media query。

```css
/* internal/devgames/theme.css */

/* 整個面板：換成公司的色序與 W 的配色 */
:root {
  --tone-1: #0ea5e9;
  --tone-2: #f43f5e;
  --sym-w: #fef3c7; --sym-w-text: #92400e; --sym-w-ring: #d97706;
}
@media (prefers-color-scheme: dark) {
  :root { --sym-w: #fde68a; }
}

/* 只有 101 號機 */
body[data-gid="101"] { --sym-l-bg: 28%; /* L 的淡底再深一點 */ }

/* 只有 101 號機的 H1 與 C1 */
body[data-gid="101"] .cell[data-sym="H1"] { --tone-base: #14b8a6; }
body[data-gid="101"] .cell[data-sym="C1"] { --sym-c-ring: #7c3aed; --sym-c-text: #6d28d9; }
```

## 在自己的專案使用

使用 problab 的專案只要準備三樣東西：延遲載入的 Lab、外掛（選填）與入口。以下以常見的結構為例：`pkg/engine` 負責 engine 接線，`internal/...` 放設定、邏輯與外掛。

### 1. 在 engine 接線加 dev 用的建構函式

PRNG、設定、邏輯與 optimal 設定都與正式環境相同，只是以 `problab.New` 加 `RegisterAll`、`Freeze` 組裝，不用 `NewAuto`（參考 `demo.NewProbLabForDev`）：

```go
// pkg/engine/problab.go
func NewForDev() (*problab.Problab, error) {
	pb, err := problab.New(pRNGFactory, cfgs, logics, optimalOption)
	if err != nil {
		return nil, err
	}
	if err := pb.RegisterAll(); err != nil {
		_ = pb.Close()
		return nil, err
	}
	pb.Freeze()
	return pb, nil
}
```

- `NewAuto` 會在啟動時預先載入所有機台的 optimal 資料；這樣組裝則是每台在第一次 Spin 才載入，dev 入口只看一兩台時不必等。
- 正式環境的建構函式維持 `NewAuto`：啟動時就能發現設定或檔案的錯誤，第一個請求也不必等載入。

### 2. 寫外掛（選填）

```text
internal/devgames/
  embed.go     // //go:embed *.js *.css  →  var FS embed.FS
  101.js       // DEV.register(101, {...})
  102.js
  theme.css    // 選填：覆寫公版配色，見上方「自訂配色」
```

- 寫法見上方「機台外掛」；demo 0～3 的外掛在 `demo/demo_devgames/`，可以當範本。
- 沒有外掛的機台照樣可以使用，只顯示公版；之後再補外掛即可。
- `/dev/meta` 以 `plugin` 標示每台有沒有外掛，前端只載入有外掛的機台。

### 3. 加入口

把 `cmd/dev/dev.go` 複製到自己的 `cmd/dev/`，只改檔案最上方的兩個注入點，下方的流程（建立與關閉 Lab、處理 Ctrl+C、呼叫門面）一行不改：

```go
var (
	pLab     func() (*problab.Problab, error) = engine.NewForDev
	devGames fs.FS                            = devgames.FS
)
```

也可以不複製，在自己的 `main` 直接呼叫門面 `server/api/dev/cli`。契約與 `playerexp/cli`、`optimizer/v2/cli` 相同：Lab 由呼叫端建立與關閉，信號由入口處理：

```go
code, err := devcli.Run(ctx, devcli.Options{
	Lab:         lab,       // 必填；呼叫端關閉
	DevGames:    devGames,  // nil 時只顯示公版
	Addr:        ":5808",   // 空字串為 devcli.DefaultAddr
	OpenBrowser: true,
	Output:      os.Stdout, // nil 時不印網址
})
```

門面以 `svrcfg.ModePanel` 啟動：只掛首頁與 `/dev`，不建立機台池。`ModeDev` 會為 `/v1/spin` 建立每台的機台池，啟動時一樣會載入全部 optimal，所以 dev 入口不要用 `ModeDev`。

Makefile 可以加一個目標：

```make
dev:
	@go run ./cmd/dev
```

### 4. 執行

```sh
make dev                                   # 開啟 http://localhost:5808/dev
go run ./cmd/dev -addr :5810 -open=false   # 換埠號、不開瀏覽器
```

- 從專案根目錄執行：optimal 目錄是相對路徑時（例如 `internal/optimal`），要從根目錄執行才找得到。
- 這個入口只有 `/dev`，沒有 `/v1/*`；需要 `/v1` 時另外啟動正式的 server。
- 寫外掛時把 `devGames` 暫時換成 `os.DirFS("internal/devgames")`，改了 js 重新整理瀏覽器就看得到，不用重新啟動；`embed.FS` 則要重新啟動。
- 沒有任何驗證機制，不得對外公開。

### 從舊版升級

| 舊的寫法 | 升級後 |
| --- | --- |
| 呼叫 `NewDevSimulator`、`DevSpinReport`、`DevSimReport` | 已刪除，編譯失敗。可重現的執行改用 `NewMachine` 加 `Machine.Spin`（一次一段）；統計改用 `NewSimulator` |
| 呼叫 `demo.NewServerConfig` | 已刪除，編譯失敗。照上方步驟組裝 |
| 自己的 dev 入口以 `server.Run` 加 `ModeDev` 啟動 | 仍然可以運作，面板照常出現；但啟動時會為 `/v1` 建立機台池、一次載入所有 optimal，也沒有外掛。建議照上方步驟改寫 |
| 呼叫舊的 `/dev/spin`、`/dev/sim` | `/dev/spin` 的格式改變（見上方 API），`/dev/sim` 已刪除；統計改用 `/v1/sim` 或 Simulator |

## 字型

介面使用 JetBrains Mono（SIL Open Font License 1.1），裝置有安裝就用本機的。授權見 `NOTICE` 與 `third_party/licenses/JetBrainsMono_OFL-1.1.txt`。
