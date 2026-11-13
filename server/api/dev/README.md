# ProbLab Dev

用瀏覽器逐段、逐步看一局的完整流程：盤面、中獎位置、每一步的贏分、選擇類遊戲的每一段，以及每台自己的特色（標記、進度條、角標等）。用途是開發期的除錯與重現，不做統計；統計請用 `make run` 或 `/v1/sim`。

> dev 路由只在 `ModeDev` 註冊，沒有任何驗證機制，**不得對外公開**。

## 啟動

```sh
make dev   # 等於 go run ./cmd/dev，自動開啟 http://localhost:5808/dev
```

前端檔案以 `go:embed` 包在 `server/api/dev/web/`，改了之後要重新啟動。

鍵盤：Space／Enter 執行 Spin，←／→ 切換上一步／下一步。格子編號平常不顯示，滑鼠移到格子上，或點一下釘住（再點一次取消）才顯示。下方時間軸依「段 > Mode > Round」分組列出每個 act，點一下就跳過去；只有一段的局不顯示段的層級。

## 頂部欄

| 欄位 | 說明 |
| --- | --- |
| 機台、押注模式 | 由 `/dev/meta` 載入 |
| 選擇方式 | 選擇類遊戲遇到選擇點時的處理方式：`first`（預設，選位置 0）、`last`（選最後一個位置）、`player`（停在選擇點，由你選） |
| 輸入 | `Seed` 或 `Snap`，明確指定，後端不從內容判斷 |
| 值 | Seed：留空自動產生；十進位整數（int64）；`hex:…`；`utf8:…`。不帶前綴的其他字串一律拒絕。Snap：某一段的 `start_b64u` |
| 局數 | Seed 時 1～1000，超過截為 1000；Snap 與 player 固定 1 |

### Seed 與 Snap

| | Seed | Snap |
| --- | --- | --- |
| 是什麼 | 建立機台的根 seed material | 某一段實際使用的 RNG 起點（結果中的 `start_b64u`） |
| 重現保證 | 同一組（機台、押注模式、選擇方式、seed、局數）結果完全相同，包含 optimal 與選擇類遊戲 | 只保證第 0 段；之後的段依選擇方式繼續，不一定與原局相同，畫面會標示 |

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
http://localhost:5808/dev?gid=2&betmode=0&choose=first&input=seed&value=5&rounds=300&spin=1&bet=137&step=6
```

`spin=1` 時載入後立即執行，再跳到第 `bet` 局的第 `step` 步（都從 1 起算）。同一組參數一定開出同一個畫面（Snap 輸入只保證第 0 段）。

## 限制

| 項目 | 限制 |
| --- | --- |
| 每次批次的局數 | 1～1000 |
| 每局的段數 | `problab.DefaultMaxSegments` |
| 請求內容大小 | 1 MB |

一批的結果整批算完才回傳，只放在瀏覽器記憶體中，不寫入瀏覽器儲存；瀏覽器只記住上次選的機台。

## API

| 方法與路徑 | 用途 |
| --- | --- |
| `GET /dev/meta` | 機台清單：gid、名稱、logic、押注單位、最大贏分、`fixed`、各 mode 的行列數與圖標名稱；另有 `max_rounds`、`max_segments`。不輸出盤面產生、賠率與中獎判定的設定 |
| `POST /dev/spin` | `{gid, betmode, choose, input, value, rounds}` → `{seed, rounds, clamped, partial_replay, total_bet, total_win, rtp, bets}`；每一局是 `{index, bet, win, isend, segments}`，`segments` 是每一段的 SpinResult |
| `POST /dev/next` | player 的續段：`{gid, betmode, cycle, choice, choose_next, cp}` → `{segment}` |
| `POST /dev/replay` | `{gid, betmode, segments: [{start_b64u, cycle, choice, choose_next, cp}]}` → `{bet}` |

- 一局的贏分是各段贏分的加總；投注只計第 0 段一次。`total_bet`、`total_win`、`rtp` 只計已結束的局。
- `choose`、`input` 必填；未知欄位、格式錯誤一律回 400，錯誤內容是純文字。
- 盤面 `screen` 是列優先：`idx = row × columns + col`；圖標 ID 是該 mode `symbols` 的索引。

## 機台外掛

每台可以有一支 `games/<gid>.js`，負責畫這台特有的東西；沒有的話只顯示公版。內建 demo 0～3 的外掛在 `web/games/`，可以當範例。

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

圖標配色的公版規則：C 白底、S 鵝黃白底；H 依編號用底色、L 依編號用字色，1→6 為紫、橘、黃、藍、紅、綠，7 以後從紫重來但變淡；W 和其他圖標用固定色；Z（空格）不寫名稱、不填底色。

### 注入自己的外掛

使用 problab 的應用在組裝 server 時，把外掛目錄交給 `svrcfg.SvrCfg.DevGames`（`fs.FS`，根目錄放 `<gid>.js`），與 `problab.Configs`、`problab.Logics` 相同的注入方式：

```go
//go:embed devgames
var devGames embed.FS

sub, _ := fs.Sub(devGames, "devgames")
cfg := &svrcfg.SvrCfg{Problab: lab, Mode: svrcfg.ModeDev, DevGames: sub}
```

`/dev/games/<gid>.js` 先找 `DevGames`，再找內建的外掛；都沒有時回 404，那台只顯示公版。同名時以注入的為準。

## 字型

介面使用 JetBrains Mono（SIL Open Font License 1.1），裝置有安裝就用本機的。授權見 `NOTICE` 與 `third_party/licenses/JetBrainsMono_OFL-1.1.txt`。
