# Release notes — Unreleased

## Player experience reports

- Preserve the five detailed CSV files and additionally publish a complete, versioned `report.json`, including all analysis groups, settings, tags, statistics and execution status. CSV and JSON are published atomically together.
- Demo Monotone strategies now use a 3× initial-balance satisfaction target and a maximum of 5,000 spins. These are demo policy settings, not engine-wide defaults.

## Breaking Go import changes

Demo package relocations require source updates for applications importing the old packages. No compatibility forwarding packages are provided.

| Previous | Replacement |
|---|---|
| `github.com/zintix-labs/problab/demo/demo_logic/game_tags` | `github.com/zintix-labs/problab/demo/demo_tags` |
| `github.com/zintix-labs/problab/demo/player_strategies` | `github.com/zintix-labs/problab/demo/demo_strategies` |
| `game_tags.GameTagCatalog` | `tag.GameTagCatalog` from `github.com/zintix-labs/problab/sdk/tag` |

Update package qualifiers to `demo_tags` and `demo_strategies`. Applications maintaining their own catalogs need not import the demo packages; use `tag.GameTagCatalog` for the shared catalog type. These relocations do not change tag predicates or YAML tag names.

## 中文升級提醒

保留五份 CSV，新增完整 `report.json`，六檔同批發布。Demo 的 Monotone 設定為本金 3 倍滿意離場、最多 5,000 局，並非引擎全域預設。

上述 demo 路徑搬移是 Go 原始碼不相容變更：引用舊路徑者必須更新 import 與套件名稱；`GameTagCatalog` 改由 `sdk/tag` 提供。自行維護 tags／策略的專案不必改用官方 demo，但應更新引用舊路徑的範例註解。YAML 標籤名稱與判斷語意不變。
