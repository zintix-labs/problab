# PlayerExp Phase 1 — 驗收紀錄

對應 PRD 0009 與 `techspec/strategy-driven-player-analysis.md`。實作與量測日期：2026-10-03；環境 Go 1.25.2、darwin/arm64。施工前主庫 revision：`45f2ac99b68ce7975cef2e415e8f7237fb08b541`。

本檔记录實作驗證，不自行將獨立文件庫的 PRD 推進為 implemented；該狀態仍由使用者確認。沒有宣稱跨平台逐 byte 相同。

## 範圍與不回歸

- 新增 `playerexp`、其 CLI、`cmd/exp` 與 demo factories；根引擎只新增 `SpinOffline`／`UsesOptimal`。
- `Spin`、`SpinInternal`、Simulator、舊 Estimator 與 optimizer 的實作未修改，沒有新增 module dependency。
- 新入口的同 seed 比對使用 native 與真實 loaded optimal manifest：各 60 局、兩種 mode、BetMult 1–3，逐局 DTO bytes 與結束 RNG snapshot 一致。這比只比總贏分更嚴格。
- `go list -deps -test ./playerexp/...` 的閉包沒有 `optimizer`／`optimizer/v2`；根套件不 import playerexp。測試也沒有用 optimizer 建 fixture。
- root README 只補新入口使用說明；`build/exp/*` 加入忽略規則，避免把包含 seed 的執行報告加入版本庫。

## 驗證指令

以下檢查均已執行通過：

```sh
GOCACHE=/tmp/problab-playerexp-cache go test ./...
GOCACHE=/tmp/problab-playerexp-cache go test -race ./...
GOCACHE=/tmp/problab-playerexp-cache go vet ./...
GOCACHE=/tmp/problab-playerexp-cache \
  GOLANGCI_LINT_CACHE=/tmp/problab-playerexp-lint golangci-lint run ./...
gofmt -l machine.go machine_offline_test.go playerexp cmd/exp demo/demo_strategies
git diff --check
GOCACHE=/tmp/problab-playerexp-cache go list -deps -test ./playerexp/...
GOCACHE=/tmp/problab-playerexp-cache make exp
```

施工前 baseline 的所有 package tests 通過，但預設 Go cache 的 trim.txt 寫入被沙箱拒絕而令整體命令退出 1；後續使用可寫的 `/tmp` cache 後，全套命令正常退出 0。不把 cache 權限錯誤當成程式測試失敗。

既有 optimizer 內依賴外部環境的 env-gated 測試可能照原設定跳過，本次不宣稱重新完成那些外部驗收。下表新增的 PlayerExp 驗收均非 env-gated。

## A01–A30 對照

測試位於 `playerexp/`，另行標示 root／cli／cmd 接點。不是僅比測試名称：右欄列出實際斷言。

| ID | 測試與證據 |
|---|---|
| A01 | `TestStrictConfig`、`TestRequiredRangeAndNumericalConfiguration`：未知／重複欄、第二文件、缺 game_id、0 合法、缺 range、非法 CI／bucket／控制項拒絕 |
| A02 | `TestSeedContractFixtures`、`TestSeedSerializationFixtures`：int64 big-endian 固定 bytes、UTF8／hex、null／省略、canonical JSON/YAML、1024 邊界、溢位與裸字串拒絕 |
| A03 | `TestPreflightAndRegistry`、`TestAllGroupsPreflightBalancesAndTags`：後組策略／tag／本金錯誤與非法 mode unit，前組 Spin／NewStrategy 均 0 |
| A04 | `TestPreflightAndRegistry`、`TestCancellationBoundariesAndNilStrategies`：跨 GID 同名合法、重複／typed-nil 拒絕、Factory 回 nil／typed-nil、tag 不跨 GID |
| A05 | root `TestSpinOfflineParity/native`：兩個 mode、三種 mult、60 局逐局 DTO 與 RNG bytes 相等 |
| A06 | root `TestSpinOfflineParity/optimal`、`TestSpinOfflineValidationAndOptimalError`、`TestSpinOfflineSnapshotRestoreFaults`：真 manifest、closed artifact、前後 Snapshot、seed／selector Restore 故障；panic 仍恢復 selector，雙重錯誤保留 |
| A07 | `TestUnregisteredExtensionReachesStrategyAndTag`：自訂遊戲檢查 isSim=false／乾淨 request，無 DTO renderer；策略及 tag 都看到 raw Extend |
| A08 | `TestStateSettlementAndDynamicRTP`：500→430→432；修改 state 副本無效；TotalWin 不重乘 |
| A09 | 同上：成本 100 與 2、贏分 30 與 4，玩家與 pooled RTP 都為 34/102，不是倍率平均 |
| A10 | `TestRealRunReproducibilityAndContinuousStream`：200 個獨立 Strategy、400 次 Next、每次 FirstSpin state／策略歷史為初始；demo factory 每人複製設定 |
| A11 | `TestDecisionValidation`、`TestGroupCommitOverflow`：mode/mult/int/uint64 溢位失敗且不 Spin／不部分 commit；合法付不起才 Bust，零投注可退出 |
| A12 | `TestDecisionValidation`、`TestPlayerLimitAndDecisionPrecedence`：忽略無關欄位，策略 Bust／未知離場拒絕 |
| A13 | `TestPlayerLimitAndDecisionPrecedence`：各跑滿 20,000 fake Spin／Next，最後策略離場優先，繼續則不驗下一注、不跑 20,001 |
| A14 | 同上與 `TestStrictConfig`：上限只增加 Others 及其子原因，YAML max_spin 拒絕 |
| A15 | `TestIncompleteResultsAndCheckpoint`：nil／未完成／帳目不符／負贏分拒絕；cp nil／typed-nil pointer 接受，空 struct/map/slice／字串拒絕 |
| A16 | `TestBucketsAndRanges`：零、首正邊界、各邊界、尾桶、[0] 與 1/3 精確比較 |
| A17 | 同上、`TestEventsAndReproducibility`、`TestOrderedTagsANDAndUnmatched`：range 特例、null、first-match、按順序 AND／短路、unmatched |
| A18 | `TestEventsAndReproducibility`：0/1/2/3 次玩家各一、頻率和 4、hits 6、零投注排除 RTP 不排除事件，後事件被遮蔽 |
| A19 | `TestPartialCancellationAndPanics`：首位提交後第二位中斷，第二位全部暫存排除、後組 NOT_STARTED |
| A20 | `TestCancellationBoundariesAndNilStrategies`、`TestPartialCancellationAndPanics`：factory/first/spin/tag/next 與 commit 邊界，errors.Is(Canceled)、partial 與進度一致 |
| A21 | `TestPartialCancellationAndPanics`、`TestUnregisteredExtensionReachesStrategyAndTag`：factory/first/next/tag/game/reporter 及建機 panic 停下，不繼續；已完成資料保留 |
| A22 | `TestConfidenceIntervals`：CP 0/1／中間／極端、95/99，n=1,k=0,95% 上界 .975 |
| A23 | `TestQuantileOrderStatisticOracle`：n=0…100、多 q／95/99；獨立 PMF 枚舉二項尾界，空／小樣本／重複值，不冒用 min/max |
| A24 | `TestRealRunReproducibilityAndContinuousStream`：同 Lab 同 seed 全 Run 重複報告相等；另開直接 Machine 逐筆 start snapshot 比對，跨玩家／組不 reseed；Lab 未被關閉 |
| A25 | `TestCSVGoldenAndRoundTrip`：五表固定 SHA256、固定 header／順序，中文／逗號／引號／換行、空 CI／尾桶與 >2^53 整數不失真 |
| A26 | `TestCSVAtomicPublicationFailures`：Create、超過 buffer 的 Write、末尾 Flush、Close、rename 故障 errors.Is；staging 清理／舊檔不變。cli partial 測試驗證取消後 CSV 狀態與精確計數 |
| A27 | cli `TestProgressTerminalAndMilestones`：100ms、10% 跨門檻只印真實值、TTY 清行／換行、一般 writer/file 不含控制字元、開始／終止不漏 |
| A28 | cli `TestCLINilOutputPartialAndErrors`、`TestCLIPartialCancellationPublishesActualCounts`：nil 仍五 CSV、文字錯誤不改玩家成功、run+I/O errors.Join、取消 1/3 不冒充完成 |
| A29 | external package `integration_test.go`、cmd `TestEmbeddedConfigAndArguments`、make exp smoke：公開組裝成功、命令拒絕 args、兩組 embedded config；依賴閉包另查無 optimizer |
| A30 | `TestPlayerStreamingMemory`、`TestPlayerMemoryRetentionBound`：百萬局、進度有無數值一致；兩種玩家／局數比例，定期 GC 後存活 heap <16MiB 防逐局結果留存 |

## Smoke 結果

`make exp` 的 native demo、seed=77；2026-10-04 依當前設定重跑：兩組各 1,000 玩家、init_bet=300、mode=0、MaxSpins=5000、SatisfiedRatio=3；monotone BetMult=1、higher_bet BetMult=2。策略參數來源為 demo/demo_strategies/game_0.go。

| 組別 | 完成玩家 | 完整 Spin | 總押注 | 總贏分 | pooled RTP |
|---|---:|---:|---:|---:|---:|
| monotone | 1,000 | 2,730,303 | 109,212,120 | 104,044,550 | 0.9526831820497579 |
| higher_bet | 1,000 | 1,028,058 | 82,244,640 | 79,852,560 | 0.9709150651033307 |

五張 CSV 與 report.json 成功發布；非 TTY 逐 10% 輸出，兩組 completed=1000/1000。這是策略下的有限樣本，不是遊戲理論 RTP。

本次輸出為 `build/exp/run-3351659966`；舊 100 局／2 倍目標的 smoke 不代表目前 demo 設定。`TestMonotoneDecisions` 驗證固定投注、實際本金倍數目標、達標優先、轉數上限為 Dissatisfied，以及可負擔性仍由執行器判斷；`TestMonotoneIndependentPlayers` 驗證設定複製與玩家隔離；`TestMonotoneRejectsInvalidSettings` 驗證參數、系統轉數上限及目標整數溢位。相關套件 test、race、vet、lint 通過。

## 記憶體與觀測開銷

單次非 race、fake 機台量測（不等於真實遊戲吞吐）：100,000 位 × 10 Spin，1,000,000 Spin。

| 進度 callback | 時間 | 累計 allocated bytes | allocations |
|---|---:|---:|---:|
| 無 | 76.7ms | 240,117,944 | 2,200,079 |
| 有（no-op） | 59.1ms | 240,107,064 | 2,200,063 |

不把一次時間差解讀為 callback 加速；重點是報告完全相等。fake 每局配置 result，所以累計分配量不是存活記憶體，也不能外推 production overhead。

另測同樣 100 萬局，每 10% 玩家完成後 GC 並取樣：1,000 位×1,000 局的最大存活 heap 增量 10,128 bytes；100,000 位×10 局為 891,864 bytes。此值是**取樣後存活量**，不是程序 RSS／精確峰值。RTP 陣列隨有效玩家数增長，沒有隨總 Spin 保存 buf。策略自己保留完整歷史仍由策略作者負責。

## 已知、刻意保留的邊界

不支援 checkpoint／續玩選擇；不自動回復 RNG 後重試；不可強制終止阻塞 callback；不偵測遊戲偷偷維護的隱藏跨局狀態；不保證不受控策略 RNG 可重現。不新增 optimizer 串接或評選器。

## 完整 JSON 報告擴充

`TestFullJSONReportPreservesGroupsAndTags` 驗證完整公開報告 round-trip、不同組的 tags、NOT_STARTED／失敗資訊及大整數；不包含私人逐玩家暫存。原 CSV golden 不變。`TestCSVAtomicPublicationFailures` 額外涵蓋 report.json create/write/close 故障，確保 CSV 和 JSON 同批發布，失敗不留下半份輸出。CLI 測試涵蓋成功／取消／文字輸出錯誤下仍有五份 CSV 與一份 report.json。
