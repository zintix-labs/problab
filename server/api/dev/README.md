# ProbLab Dev

用瀏覽器逐段、逐步看一局的完整流程：盤面、中獎位置、每一步的贏分、選擇類遊戲的每一段，以及每台自己的特色（標記、進度條、角標等）。用途是開發期的除錯與重現，不做統計；統計請用 `make run` 或 `/v1/sim`。

本文的「段」是一次 `Machine.Spin` 的結果（一個 SpinResult）。一般遊戲一局只有一段；選擇類遊戲在選擇點把一局切成多段，選完再送續段請求。面板上一律標成 Cycle，與 DTO 的 `cycle` 欄位同名。

> dev 路由只在 `ModeDev` 與 `ModePanel` 註冊，沒有任何驗證機制，**不得對外公開**。

## 啟動

```sh
make dev   # 等於 go run ./cmd/dev，自動開啟 http://localhost:5808/dev
go run ./cmd/dev -addr :5810 -open=false   # 換埠號、不開瀏覽器（例如同時開兩個）
```

Ctrl+C 結束；仍有連線時最多等 5 秒。

`make dev` 以 `svrcfg.ModePanel` 啟動：只掛首頁與 `/dev`，沒有 `/v1`，也不建立機台池；engine 以 `problab.New` 加 `RegisterAll`、`Freeze` 組裝，所以每台的 optimal 資料在第一次 Spin 才載入，不會在啟動時一次載入全部。要同時使用 `/v1/*`，請用 `make svr`（`ModeDev`）。

公版前端以 `go:embed` 包在 `server/api/dev/web/`，改了之後要重新啟動。

## 操作

頂部欄最右邊的鍵盤圖示（或按 `?`）列出所有快捷鍵與滑鼠操作；它左邊的主題按鈕依序切換跟隨系統、淺色、深色；再左邊的圖表按鈕（或按 `S`）打開玩家統計；最左邊的「資金」是玩家剩餘資金，按一下也會打開玩家統計。

| 按鍵 | 功能 |
| --- | --- |
| Space／Enter | Spin |
| ←／→ | 上一步／下一步 |
| Shift+←／→ | 上一個／下一個 round |
| Home／End | 第一步／最後一步 |
| ↑／↓ | 上一局／下一局：只在局列表看得到的局之間切換（勾選「只看 ≥ X 倍」時跳過沒達到門檻的局），到頭不繞回 |
| P | 播放／暫停 |
| + | 速度切到下一檔，turbo 之後回到 normal |
| I | 切換 SymbolName／SymbolID |
| S | 開關玩家統計；面板開著時只有 S 和 Esc 有作用 |
| Esc | 說明開著時關閉說明，否則清掉所有釘住的格子編號 |
| ? | 開關說明 |

在輸入框與選單裡打字時不攔按鍵；Enter 在輸入框裡也會執行 Spin。

格子編號平常不顯示，滑鼠移到格子上，或點一下釘住（再點一次取消）才顯示。

局列表上方的「只看 ≥ X 倍」勾選後只列出贏分大於 0、而且贏倍（贏分 ÷ 投注，也就是列表上括號裡的數字）達到 X 的局，X 預設 0.01；填 0 等於只看有贏分。改門檻時會自動勾選，瀏覽器會記住勾選狀態和門檻。Spin 後自動選的第一局、↑↓ 換局與連續播放都照這個篩選走。

盤面下方的控制列分三組：左邊是步進（⏮ ◀ 目前步數 ▶ ⏭），中間是播放（播放、速度、單局／連續），右邊是檢視與分享（SymbolName／SymbolID、複製連結）。兩段切換的按鈕只寫目前的值，按一下切換。

播放速度固定三檔：normal（預設，每步 1350 ms）、fast（650 ms）、turbo（200 ms）。換步時盤面會播落下動態（見下方「落下動態」），normal 與 fast 依速度縮短，turbo 不播；系統設定「減少動態效果」時也不播。盤面下方亮幾個箭頭就是第幾檔，點第幾個就切到第幾檔。預設是「單局」，只播完這一局；切到「連續」後接著播局列表的下一局（同樣依「只看 ≥ X 倍」篩選），播到最後一局或停在選擇點（player）的局就停，不會自動 Spin。手動換局會停止播放，手動換步不會。

盤面下方的 SymbolName／SymbolID 按鈕（或按 I）切換格子與「本步中獎」的圖標欄要寫名稱（`H1`）還是盤面上的 ID（該 mode `symbols` 的索引，也就是後端陣列裡的數字）；ID 模式下空格與不存在的格子也寫出原始值，方便逐格比對。滑鼠移到格子上一律同時顯示兩者，瀏覽器會記住上次的選擇。下方時間軸依「Cycle > State > Round」分組列出每個 act，點一下就跳過去；只有一段的局不顯示 Cycle 的層級。Mode 的標題寫成 `State 1 · ModeID 2 [FreeGame]`：State 是這一段 `gamemodes` 裡的第幾個（每一段各自從 0 起算），ModeID 是設定檔 `game_mode_settings` 的索引，方括號裡是外掛提供的名稱，沒有外掛或外掛沒寫時省略。資訊列的 State 欄用同一個格式。

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

Spin 之後，Spin 按鈕旁會出現「seed」按鈕，按一下複製這批實際使用的 seed（滑鼠移上去看得到值），可以原樣貼回重現；Seed 留空時就用它找回自動產生的 seed。

## 回放

| 方式 | 做法 | 範圍 |
| --- | --- | --- |
| 回放這局 | 右側「本局 DTO」的按鈕：以這局每一段記錄的 `start_b64u`，加上續段的 `choice`、`choose_next` 與 `cp`，逐段重跑並比對 | 整局完全重現，包含 optimal 依入口抽庫的續段 |
| 用這個快照重跑 | 右側「Cycle 0 快照」的按鈕（只有第 0 段有）：輸入切到 Snap、填入 `start_b64u` 後執行 | 只保證第 0 段 |
| 手動貼 Snap | 從正式後端的紀錄貼上某一局第 0 段的 `start_b64u` | 只保證第 0 段 |

一局有多段時，單一快照無法還原續段的輸入，所以要完整重現必須用「回放這局」。

右側面板的「Cycle N 快照」是目前這一步所在那一段的 `start_b64u`、`after_b64u` 與 `cp`，換步到別的段時跟著換；「本局 DTO」是整局的回放、複製與完整 JSON。

## player 模式

選擇方式選 `player` 後按 Spin，這局停在選擇點時，時間軸末端會列出選項（`#位置 → 入口值`）。按下後送出一個續段請求，新的段接在同一局後面；局結束後左側總計才計入這一局。續段的格式與正式環境相同，伺服器不保存狀態；續段的起點依機台的 RNG 政策取得，每一段都會記下自己的 `start_b64u`，事後一樣可以「回放這局」。

## 玩家統計

把手動按 Spin 的局累計成一個玩家 session，跨機台、跨押注模式與倍數一路累計，直到按「歸零」為止，可以拿來測試玩家的跨機台、跨模式策略。

- **計入哪些局**：只有在頁面上手動按 Spin（按鈕、Space 或 Enter）、輸入為 Seed 的局。從網址自動執行的 Spin（`spin=1`）、歷史局的回放連結、Snap 與「回放這局」都是在重現已知的局，不計入。player 的局選到結束才計入，中途又按 Spin 的局不計入。
- **數字**：轉數、總押注、總贏分、資金 RTP、倍率 RTP、進 FG 的局數（括號是平均幾局一次）、目前／最高／最低資金、最大贏倍；資金第一次跌破 0 時另外標出第幾局破產。
- **兩種 RTP**：資金 RTP 是總贏分 ÷ 總押注；倍率 RTP 是每局贏倍（贏分 ÷ 該局押注）的平均。例如兩局，押 20 贏 0、押 100 贏 120：資金 RTP 100%，倍率 RTP (0 + 1.2) ÷ 2 = 60%。押注額不變時兩者相同。
- **資金**：起始資金預設 10000，可以修改，瀏覽器會記住。資金 = 起始資金 + 累計淨輸贏，頂部欄右側一直顯示目前的剩餘資金；本次統計中只要破產過（最低資金跌破 0），數字就變成紅字，之後賺回來也一樣，滑鼠停在上面會寫出最低資金；改起始資金時整條曲線與最高／最低資金一起平移，資金可以是負數。
- **走勢圖**：山形圖，線下方是淡出的漸層；一次畫一條線，可切換「資金」「資金 RTP」「倍率 RTP」，圖例旁寫出目前這條線的算法。圖涵蓋整個 session，不只保留的 10,000 局：保留範圍內每局一點，更早的部分用走勢資料，每點涵蓋多局（圖例旁會寫「早期每點 N 局」），區間的起點與終點只能落在點上，但點上的數值是精確的。虛線是起始資金與 0（資金）或 100%（RTP）；破產的位置標紅點。RTP 在開頭幾局起伏很大，縱軸範圍略過開頭 5%（最多 200 局），超出的部分在圖框邊緣截掉。滑鼠移到圖上顯示該點的數值。
- **區間**：在走勢圖上按住滑鼠拖曳（觸控是長按 0.3 秒再拖），讀數列顯示兩點之間的變化：局號與局數、目前這條線從起點到終點的變化（資金寫差額與百分比，RTP 寫百分點）、區間押注、區間贏分、區間資金 RTP、區間倍率 RTP、區間進 FG 次數、區間最大贏倍與最大回撤（資金從區間內高點往後掉最多的幅度）。選取範圍在圖上是一條淺色底帶；讀數列的變化往上是綠字、往下是紅字。放開後保留選取，點一下圖或按 Esc 清掉（有選取時 Esc 先清選取，再按一次才關閉面板）。
- **歷史局**：最近 10,000 局，由新到舊，欄位是局號、機台、BetMode、FG、押注、贏倍，可以用「只看 ≥ X 倍」篩選（與局列表的篩選分開）。「開新視窗」以回放連結在新分頁重播那一局，「複製連結」複製同一個網址。超過 10,000 局時只刪掉最舊的紀錄，數字與走勢圖仍然涵蓋整個 session；破產發生在保留範圍之前時，只知道落在走勢資料的哪一點，顯示為「第 a～b 局之間」。
- **歸零**：按兩次（第一次變成確認）清空所有統計與歷史局，重新開始累計。

統計存在瀏覽器的 IndexedDB，重新整理後還在；同一個瀏覽器的所有 dev 分頁共用一份，同時在多個分頁 Spin 時紀錄會交錯。瀏覽器不能用 IndexedDB 時統計停用，其他功能照常。每局存的是整局的回放輸入（每一段的 `start_b64u`，續段另有 `choice`、`choose_next` 與 `cp`）與結果的雜湊，不存盤面，所以 1 萬局只佔幾 MB。走勢資料另外存一份，最多 2048 格，滿了就相鄰兩格合併、每格涵蓋的局數加倍，所以不管玩多少局都只佔幾百 KB；每格記錄結尾的累計值、格內的最高／最低點、最大回撤與最大贏倍，合併後峰谷與回撤不會被抹平。從舊版升級時，走勢資料只能用當時保留的紀錄補出來。改過遊戲設定或程式之後，舊紀錄重播出來的結果可能和紀錄不同，這時狀態列會顯示警告。

## 網址參數

```
http://localhost:5808/dev?gid=2&betmode=0&betmult=1&choose=first&input=seed&value=5&rounds=300&spin=1&bet=137&step=6
```

`spin=1` 時載入後立即執行，再跳到第 `bet` 局的第 `step` 步（都從 1 起算）。同一組參數一定開出同一個畫面（Snap 輸入只保證第 0 段）。加上 `symmode=id` 會以 ID 模式開啟。

歷史局的回放連結改帶整局的回放輸入，只跑這一局，player 的選擇也能完全重現：

```
http://localhost:5808/dev?gid=2&betmode=0&betmult=1&replay=<整局回放輸入>&h=<結果雜湊>&n=18&step=3
```

`replay` 是每一段回放輸入的 JSON 以 base64url 編碼，`h` 是紀錄當時的結果雜湊（重播結果不同時狀態列警告），`n` 是 session 裡的局號，`step` 選填。從這種連結打開的頁面，「複製連結」也產生同一種網址。

盤面下方的「複製連結」會把目前這一步組成上面的網址（seed 留空時填入實際使用的 seed），用來回報問題。player 的選擇不在網址裡，所以 player 的局只重現到第 0 段；這種局與 Snap 輸入的多段局，按鈕旁會出現 ⚠，滑鼠移上去看說明。

## 限制

| 項目 | 限制 |
| --- | --- |
| 每次批次的局數 | 1～1000 |
| 投注倍數 | 1～100 |
| 每局的段數 | `problab.DefaultMaxSegments` |
| 請求內容大小 | 1 MB |

一批的結果整批算完才回傳，只放在瀏覽器記憶體中；瀏覽器另外記住上次選的機台、SymbolName／SymbolID、主題、贏倍篩選與起始資金，玩家統計的紀錄存在 IndexedDB（見「玩家統計」）。

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
  modes: { 0: "BaseGame", 1: "FG-A", 2: "FG-B" }, // 每個 ModeID 的名稱，顯示在時間軸與資訊列
  css: ".cell.blast { outline-color: red; }", // 這台專用的樣式，選填
  panel(ctx) { return "<html>"; },     // 右側面板內容
  cellClass(idx, ctx) { return []; },  // 格子要加的 class，例如 "mark"、"dim" 或自訂的
  label(idx, ctx) { return ""; },      // 格子中央的文字，空字串用預設
  badge(idx, ctx) { return ""; },      // 格子右下角的小字
  actLabel(act, ctx) { return act.acttype; }, // 時間軸上每一步的文字
  motion(ctx) { return DEV.util.motion(ctx); }, // 盤面的落下動態，見下方「落下動態」
});
```

每個欄位都可以省略。`ctx` 有：

| 欄位 | 內容 |
| --- | --- |
| `game` | `/dev/meta` 的這台資料（含 `fixed`、`modes`） |
| `bet` | 這一局：`{index, bet, win, isend, segments}` |
| `segment`、`segIdx` | 目前這一步所在的段（SpinResult）與段號 |
| `mode`、`modeIdx` | 目前的 game mode（`modeid`、`trigger`、`win`、`acts`），以及它在這一段 `gamemodes` 裡的索引（畫面上的 State） |
| `act` | 目前這一步（`acttype`、`round`、`step`、各種贏分、`screen`、`details`、`ext`） |
| `ext`、`prevExt` | 這一步和上一步的 ext；這一步沒有 ext 時，沿用這局最近一次出現的值 |
| `screen`、`prevScreen`、`prevAct` | 這一步和上一步的盤面與 act；沒有盤面的步驟沿用同一個 mode 的前一個盤面 |
| `modeInfo`、`symbols` | 這個 mode 的行列數和圖標名稱 |

`label` 用在格子帶了比圖標名稱更重要的數值的機台，例如現金、倍數：回傳金額當中央的大字，原圖標名稱改由 `badge` 寫在角標。

- 回傳非空字串時取代中央的圖標名稱；回傳 `""`、`null`、`undefined`、`false` 或沒定義 `label` 時照舊顯示（Z 系列空格不寫名稱）。回傳數字會轉成字串。
- 文字和 `badge` 一樣當純文字顯示，`<`、`&` 不會變成 HTML。
- ID 模式（SymbolID，`symmode=id`）不呼叫 `label`，一律顯示原始 ID；ID 模式是用來和後端陣列逐格比對的，不能被外掛蓋掉。
- 負值格（不存在的格子）不呼叫 `label`。
- `data-sym`、滑鼠提示（idx、名稱、id）、配色與覆寫規則都照原圖標；`cellClass`、`badge`、中獎框與動態不受影響。
- 改寫的格子帶 `labeled` class，字級統一為格子寬的 0.24（L 系列也不放大）；5 個字以上再加 `long`，自動縮小。要再調整時在外掛的 `css` 寫 `.cell.labeled { … }`。demo 0 的外掛示範了這個用法。

`modes` 與 `actLabel` 是替玩法和動作取名，屬於畫面的工作，不寫進遊戲設定檔。撰寫外掛時（包括請 AI 協作時），先列出這台每個 ModeID 的行列數、圖標與觸發方式，請使用者決定每個 ModeID 的名稱，並依這些資訊提供建議選項（例如觸發 FG 的 mode 叫 `FreeGame`）；`actLabel` 的文字也先問過再寫。

工具函式：`DEV.util.kv(rows)`（兩欄表）、`DEV.util.progress(value, total, label)`（進度條）、`DEV.util.sym(ctx, id)`（圖標 ID 轉名稱）、`DEV.util.motion(ctx)`（公版推斷的落下動態）、`DEV.util.esc(s)`。

### 落下動態

換步時，公版比對這一步和上一步的盤面，推斷格子怎麼移動，不看 `acttype`，所以沒有外掛的機台也有：

| 情況 | 動態 |
| --- | --- |
| 每個 round 的首盤 | 整盤從上方落下，由左到右一欄一欄依序落下，一邊落下一邊淡入 |
| 某一欄上一步有被拿掉的格子，這一步剩下的圖標順序不變、全部落在這一欄最底下 | 剩下的圖標從原本的列滑下來；上方新出現的圖標從盤面上緣補進來。受影響的欄同時落下，沒變的欄不動 |
| 其他變化（圖標變身、固定格、往側邊掉等） | 不播落下，只有新圖標的亮光 |

「被拿掉的格子」是上一步的空格（Z 系列圖標，例如 `clear` 之後的盤面）；上一步沒有空格時，改用上一步的中獎格，所以把消除、下落、補牌放在同一個 act 的遊戲也適用。負值格（不存在的格子）不動，圖標可以跨過它，與 `ops.Gravity` 相同。往下滑的舊圖標不加新圖標的亮光；落下的格子等落定後才開始亮光與中獎框的 pop。

規則不同的機台用外掛的 `motion(ctx)` 指定，只在換步時呼叫：

| 回傳值 | 效果 |
| --- | --- |
| `false` | 這一步不播落下 |
| `undefined`、`null` | 用公版推斷 |
| 物件或 `Map`，`idx` → `{dx, dy, order, fade}` | 只播列出的格子 |

- `dx`、`dy`：起點相對終點差幾格，可以有小數；`dy` 負數是在上方，`dx` 負數是在左邊。省略為 0。
- `order`：依序落下的順位，0 最先，越大越晚；實際間隔依速度換算，全部錯開不超過 normal 350 ms、fast 180 ms。省略為 0。
- `fade`：新出現的圖標，一邊移動一邊淡入，也照常加上新圖標的亮光；省略時視為舊圖標移動，不加亮光。

例如往左掉的遊戲，可以先拿公版結果再換方向，或整個自己算：

```js
motion(ctx) {
  if (ctx.act.acttype !== "slide_left") return DEV.util.motion(ctx);
  const cols = ctx.modeInfo.columns;
  const m = new Map();
  ctx.act.ext.moves.forEach(([from, to]) => m.set(to, { dx: (from % cols) - (to % cols) }));
  return m;
},
```

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

H、L 的色序 1→6 為紅、橘、紫、藍、綠、灰，由暖到冷再到灰，像稀有度由高到低；7 以後從 1 重來但變淡。特殊圖標（W、C、S）都有 2px 外框，中獎時外框照樣保留。

### 可以覆寫的變數

| 變數 | 用途 | 淺色預設 | 深色預設 |
| --- | --- | --- | --- |
| `--tone-1`～`--tone-6` | H、L 的色序 | `#ef4444`、`#ff8000`、`#a855f7`、`#3b82f6`、`#65a30d`、`#64748b` | 同左 |
| `--tone-1-text`～`--tone-6-text` | H 的字色 | 皆為 `#fff` | 同左 |
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

深色的預設值寫在 `:root:where([data-theme="dark"])` 裡：面板依頂部欄的主題按鈕（跟隨系統、淺色、深色）在 `<html>` 設定 `data-theme="light"` 或 `"dark"`。`theme.css` 只寫 `:root` 時，深色也會套用同一個值；深色要用不同的值，就在 `theme.css` 裡另外寫 `:root[data-theme="dark"]`。不要用 `@media (prefers-color-scheme: dark)`：手動切換主題時它不會跟著變。

```css
/* internal/devgames/theme.css */

/* 整個面板：換成公司的色序與 W 的配色 */
:root {
  --tone-1: #0ea5e9;
  --tone-2: #f43f5e;
  --sym-w: #fef3c7; --sym-w-text: #92400e; --sym-w-ring: #d97706;
}
:root[data-theme="dark"] { --sym-w: #fde68a; }

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
