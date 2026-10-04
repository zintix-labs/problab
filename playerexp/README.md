# 有限玩家體驗分析（Phase 1）

`playerexp` 回答「有限遊玩體驗中，玩家遇到什麼、如何離場」，不是長期 RTP 模擬器或最佳策略搜尋器。保留既有 Simulator／Estimator；本套件不依賴 optimizer，未來要接 evaluator 另行實作。

## 執行

編輯 `cmd/exp/exp_cfg.yaml` 後執行 `make exp`。指令不接受參數；預設產物在 `build/exp/run-<唯一識別>/`。終端機顯示原地更新的完成玩家數，檔案／管線輸出使用 10% 里程碑；不會為跳過的門檻捏造進度。

Go 呼叫端可使用 `ParseConfig` → `Run(ctx, config, Options)` → `WriteCSV`；也可用 `playerexp/cli.Run` 組合執行、進度與 CSV。`Output=nil` 只關閉文字；`OutputDir` 可改發布目錄。CLI 回傳 0 表示成功，錯誤或取消回傳 1 與 error。Lab 由呼叫端建立、關閉；分析不接管其生命週期。

策略註冊為 `GameID → 名稱 → StrategyFactory`。同名可以分別註冊給不同遊戲；沒有自動跳過或自動註冊的策略。參考 `demo/demo_strategies`：Factory 保存遊戲設定，每位玩家建立獨立 Strategy。自訂策略若需要近期歷史，只複製需要的數值，不保留機台重用的結果指標。Tags 由 `Options.Tags` 注入，也依遊戲隔離；沒有內建 bg／fg。

宣告式組裝使用 `StrategyCatalog map[spec.GID][]StrategyRegistration`，每筆為 Name／Factory；
`BuildRegistry()` 沿用 Registry 的驗證，按 GID 排序、按清單順序處理，失敗不回傳部分 Registry。
官方可選策略放在 `playerexp/strategies`；Demo 的 `game_0.go` 選用策略與參數，`reg.go` 集中遊戲對應；
新增遊戲清單後，在 reg.go 加一行即可。不使用全域 init 自動註冊。
`cmd/exp/main.go` 頂部集中 `strategyCatalog`、`gameTags`、`pLab` 三個注入點；
私有專案替換這些來源與 YAML，不必修改下方執行流程。Catalog map／slice 不與建出的 Registry 共用，
但 Factory 本體仍是借用，執行期間不可修改。

官方 `strategies.Monotone` 固定 BetMode／BetMult，餘額達初始本金的 SatisfiedRatio 倍即滿意離場；
未達標而完成 MaxSpins 則不滿意離場，同時達成時滿意優先。無法負擔下一注仍由執行器記 Bust。
MaxSpins 必須在 1…20,000，SatisfiedRatio 為大於 1 的整數。Factory 收到的是已換算的實際本金，
不需要 BetUnits；非法設定／目標金額溢位會 panic，由 Run 的 factory 邊界轉成 error，不計為正常離場。
私有策略可放在應用的 `internal/selfstrategies`，實作相同介面後加入遊戲清單；不注入遊戲算分邏輯，
也不由核心自動註冊。`Monotone` 不需要保存逐局歷史；其他策略需要時應只複製所需欄位。

## 投注與資料所有權

- 初始本金為 `init_bet * BetUnits[0]`；每次成本是指定 mode 的 BetUnit × BetMult，贏分直接使用 TotalWin，不再次乘 BetMult。
- `FirstSpin` 收到初始帳本；`Next` 收到已完成本局結算的 `PlayerState` 數值副本。策略不必維護第二本帳。
- `Decision.KeepPlay=true` 使用 Bet、忽略 EndType；false 使用 Other／Satisfied／Dissatisfied、忽略 Bet。Bust 只由執行器判定：想繼續但付不起下一筆合法投注。非法投注是錯誤，不是破產。
- `*buf.SpinResult` 與巢狀緩衝是唯讀借用，到下一次 Machine 操作便可能失效。保存必要欄位請複製；需要整局時可自行用 `dto.NewSpinResultDTO`，Extend 的獨立性仍取決於遊戲 Snapshot 契約，執行器不自動轉 DTO。
- Registry、Factory、tag closure 及設定不得在 Run 中被外部修改。策略／tag 不應修改結果或引擎 RNG。額外使用時間、網路或其他 RNG 的策略須自行控制，否則不保證重跑一致。

## 第一階段邊界

一次 Run 使用一台 deterministic、`isSim=false` 機台，保留 Extend，依序處理組別與玩家，亂數流連續前進。尊重 Lab 的 native／optimal 選擇，報告逐 mode 記來源。沒有暖機、平行玩家、跨組重播、checkpoint 或 Choice。完整 FG／連消可以包含在同一次 Spin 裡；每次結果必須 IsGameEnd 且沒有 checkpoint。

每位玩家最多 **20,000 個完整 Spin**。這是有限體驗視窗的產品邊界，不是統計定理或效能限制；更長期的分布估計請用 Simulator。第 20,000 局照常結算並呼叫 Next，策略離場優先；仍想繼續則 Others／max_spin_reached，不執行下一注。

取消、非法結果或 callback／遊戲 panic 會停止整次 Run，丟棄當前玩家全部暫存統計。先前完成玩家保留，後組 NOT_STARTED。Run 同時回 partial Report 與 error，CLI 仍嘗試發布 CSV，發布失敗會合併原錯誤。同步 callback 無法被 context 強制中斷；需等其返回才能觀察取消。

## 桶、事件與統計

`win_buckets: [0,0.5,1,2]` 表示零獎、(0,0.5)、[0.5,1)、[1,2)、[2,+∞)。倍率為實際總贏／總押。`win_range` 恰兩個端點，必須是 bucket 邊界；null 上界代表無界。

| range | 意義 |
|---|---|
| [0,0] | 只有零獎 |
| [0,第一個正邊界] | 排除零獎的第一正桶 |
| [0,更大邊界] | 包含零獎的連續桶 |
| [a,b]，a>0 | [a,b) |
| [a,null] | a>0 為 [a,+∞)；a=0 包含所有結果 |

事件按順序 first-match，tags AND；一局最多命中一個事件。每位玩家結束後，各事件增加一次 zero／one／two／more（3+）頻率，各事件人數之和等於 Completed。零投注離場也包含在事件和離場統計，但不包含在玩家 RTP。

玩家 RTP = 該玩家總贏／總押，每位玩家等權；pooled RTP = 全組總贏／總押，不是玩家 RTP 或逐局倍率的平均。只保存有效玩家的單一 RTP 值與目前玩家記錄，不保存整批逐局結果。

`confidence_95`（預設）及 `confidence_99`：玩家比例採雙側 Clopper–Pearson；RTP 分位數點估計索引是 floor(q*n)，區間採二項秩反演。樣本不足以給有限區間時標示 unavailable，不拿 min/max 冒充。個別 CI 不是整份報告的聯合覆蓋保證；需假設適用 RNG 與玩家隔離，固定 seed 本身不證明獨立。桶比例與 pooled RTP 不估 CI。

## CSV 欄位

五表皆為 UTF-8、固定 header、LF、十進位整數與可 round-trip float64；共用前綴 `group_index,group_name,group_status`。空欄不是零，應搭配 defined／available／reason 判斷。無界 max 留空。

| 檔案 | 內容／分母 |
|---|---|
| summary.csv | section/key/value 長表：canonical seed、模式來源、設定、狀態、目標／啟動／完成／未完成人數、整數帳與 pooled RTP |
| win_buckets.csv | 每桶 count、CompleteSpins 分母、probability、觀測最大值；沒有 CI |
| player_rtp.csv | quantile (.1,1/3,.5,2/3,.9) 與 threshold (<=.3/.5/.7/1)；n 只計總押>0 玩家 |
| events.csv | 每事件四列遭遇頻率，分母 Completed；hit_count 是事件總命中數，在四列重複，**不可相加** |
| exits.csv | bust/satisfied/dissatisfied/others；max_spin_reached 是 others 子原因，**不是第五個互斥類別** |

NOT_STARTED 組只保留 summary，不偽造完成統計。FAILED／CANCELED 組的表格只包含已提交玩家並標示狀態。五表與 report.json 先寫私有 staging，全部寫入／Close 成功才一次發布；不覆寫舊報告，失敗清本次 staging。

## 完整 JSON 報告

`WriteCSV` 與 CLI 現在同時輸出 `report.json`，現有五張 CSV 的欄位與內容不變。JSON 外層為 `schema_version: "playerexp-report-v1"` 與 `report`；內層使用公開 Report 型別的欄位名稱（例如 `Groups`、`Config`、`Events`）。包含所有分析組、設定、事件 tags、統計、模式來源與失敗／取消狀態，不輸出私人逐玩家 RTP 暫存。零值是否有效須看 `Defined`，CI 是否可用須看 `Available`；無界 Max 為 null。事件頻率陣列依 zero/one/two/more 排列，離場陣列依 bust/satisfied/dissatisfied/others 排列。

不同分析組的 tags 不合併：CSV 以 group_index/group_name 區分，事件的 tags 定義在 summary.csv，命中分布在 events.csv。JSON 則可在同一個 Group 下查看 Config.Events 與 Events。多 tags 是 AND，同局多事件採 first-match。JSON 整數以十進位完整寫出；JavaScript 消費端若需超過 2^53 的整數精度，應使用支援大整數的解析器。

報告包含可重跑 seed，不應交給不該取得種子材料的對象。設定中的名稱可含試算表公式字元；CSV 跳脫不是公式注入防護，應以文字匯入，不會偷偷更改原始名稱或 seed。

驗收矩陣與實測記錄見 [VERIFICATION.md](VERIFICATION.md)。
