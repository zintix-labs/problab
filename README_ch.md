🌐 语言：中文 | [En](README.md)

---

# Problab

<sub>由 <b>Zintix Labs</b> 维护 — <a href="https://github.com/nextso">@nextso</a></sub>

**Problab** 是一个面向数学设计师和工程师的高性能老虎机数学引擎。

一次构建，随后即可使用**相同的单一事实源**  
进行**模拟、复现及生产环境部署**。

**模拟执行路径与生产环境完全一致。**

---

## 什么是 Problab？

**如果在模拟中能运行，**  
**它在生产环境中也能以相同方式运行。**

Problab 是一个**老虎机数学执行引擎**，  
同时适用于大规模模拟和真实生产环境。

它是**同一个引擎**，可用于：

- 大规模数学模拟
- 基于种子的确定性复现
- 后端游戏服务器执行
- 开发与调试

不存在**“模拟逻辑”与“生产逻辑”的分离**。

---

## 为什么选择 Problab？

传统老虎机数学引擎通常存在以下问题：

- 模拟器速度快但**不适合生产环境**
- 生产引擎正确但**模拟速度过慢**
- 模拟逻辑与服务器逻辑**随时间发生偏离**
- 真实生产问题难以复现或无法复现

Problab 致力于解决上述核心难题。

---

## 核心设计目标

- **单一事实源**  
  一个引擎，一套逻辑路径，一致结果。

- **天生高性能**  
  零分配的关键路径，缓存友好的数据布局。

- **显式依赖注入**  
  无隐藏全局，无魔法初始化副作用。

- **开发者友好**  
  新增游戏只需提供：
  - 一个配置文件
  - 一个逻辑文件

- **模拟与生产执行路径一致**  
  模拟非伪造或重写，  
  运行与生产环境完全相同的确定性逻辑路径。

---

## 性能快照（MacBook Air M3）

以下数据均为**真实测量值**，非合成基准。

### 单核性能

| 游戏类型 | 吞吐量 |
|----------|------------|
| 简单（5x3，15条线） | 约 700 万转/秒 |
| 级联 / 聚集 | 约 170 万转/秒 |

### 四核并行执行

| 游戏类型 | 吞吐量 |
|----------|------------|
| 简单 | 约 1900 万转/秒 |
| 级联 / 聚集 | 约 580 万转/秒 |

### 实际运行示例输出

```bash
make run w=4 r=25000000
```

```text
[WORKERS:4] [GAME:demo_normal] [PLAYMODE:0] [SPINS:100,000,000]
used: 5.26 seconds                                                                                                                      
sps : 19,010,181 spins/sec
+--------------------------------+
|          demo_normal           |
+--------------+-----------------+
| Game Name    | demo_normal     |
| Game ID      | 0               |
| Total Rounds | 100,000,000     |
| Total RTP    | 95.56 %         |
| RTP 95% CI   | [95.42%,95.69%] |
| Total Bet    | 4,000,000,000   |
| Total Win    | 3,822,201,660   |
| Base Win     | 2,439,779,100   |
| Free Win     | 1,382,422,560   |
| NoWin Rounds | 71,034,259      |
| Trigger      | 835,933         |
| STD          | 6.797           |
| CV           | 7.113           |
+--------------+-----------------+

```

> 以上数据基于真实游戏逻辑  
> （线型中奖、免费游戏、级联、重力、倍数等）

---

## 典型使用场景

- 老虎机数学模拟与验证
- RTP / 波动率分析
- 确定性重放与调试
- 后端游戏服务器执行
- 数学变更的持续集成回归测试

---

## 内置数学报告（统计与验证）

Problab 提供一流的**数学验证报告**，用于校验与回归。

开箱即用，支持生成：

- **RTP 及 95% 置信区间**
- **标准差(STD) / 变异系数(CV)（波动率）**
- **命中率 / 未中奖率 / 触发率**
- **中奖分布区间**（基础 / 免费游戏 / 总计）
- **录制器风格汇总**，支持审计和调试流程

设计理念是实用的数学工作流程：  
**验证 → 比较 → 回归 → 解释**，且输入可复现。

Problab 将**验证输出**视为一等产品特性，而非附属功能。

**示例输出**

```
总RTP: 95.56%
RTP 95%置信区间: [95.42%, 95.69%]
标准差(STD): 6.797
变异系数(CV): 7.113
...
```

### 内置 Optimizer（`optimizer/v2` + `cmd/opt`）

Problab 提供一套 **以线性规划（LP）为核心的数学设计 Optimizer**，接入 CLI 入口
`cmd/opt`。它不是曲线拟合或「生成候选再筛选」的工具——它把设计师写的 YAML
当成一份明确、可分类型别的意图契约，先证明可行性，才会回传结果：

- **离散、语意化建模**——结果被分组成 `Class` 与 `atomic bucket`，并有明确的
  `Main Group` / `Other` 可见度语意，而不是预设一个连续或常态分布形状。
- **Hard/soft 从架构上就分开**——设计师的 hard 条件（精确均值、中位数区间、
  CV 区间、Main 总量、碰撞风险上限）必须被精确满足，否则回传带诊断原因的
  typed `INFEASIBLE_*` 状态；只有明确宣告的 soft 偏好（Main profile 形状、
  bucket 能见度）才允许有取舍，而且取舍多少会被量化回报并锁定，不会被悄悄
  吸收掉。
- **不做静默放宽**——无解就是无解：optimizer 不会自动放宽容忍度、不会换个
  seed 重试、也不会为了硬凑出一个结果而丢掉某条约束。
- **系数来自真实收集样本，不是区间中点**——每一条 LP 系数（均值、二阶矩、
  CDF）都来自透过上述同一条执行路径实际模拟出的 spin 结果，并在发布前重新
  playback 验证。

可用 `go run ./cmd/opt`（搭配 `cmd/opt/opt_cfg.yaml`）执行，也可以直接把
`optimizer/v2` 当作库来用。

#### 可重放 Collection Bank

昂贵的样本收集结果现在可以重复利用，同时不需要信任旧有的 payout 或分类元数据。
在 RunPlan 中按重放优先顺序配置零个或多个 raw Core snapshot bank：

```yaml
collection:
  collected_seed:
    - build/optimizer/collected/game_0/mode_0/seed_bank_1788772008_s4.bin
  workers: 4
  batch_size: 500000
  max_spins: 10000000000
```

Optimizer 会以串流方式读取每个 bank 中的固定长度 snapshot，将其还原到当前 raw
Machine，再执行当前游戏与 bet mode，并重新套用当前 Tag 及首个匹配的 Class 规则。
相对路径以命令启动时的工作目录为基准解析。

- Replay source 按配置声明顺序处理。实际读取的来源之间会按完整 snapshot bytes
  去重，第一次出现者优先。Fresh 并行收集保留既有由 PRNG 定义的行为，不会与
  replay 或其他 worker 在收集热路径中即时去重。
- Replay records 不消耗 `max_spins`。Replay 接受的结果会先填入各 Class quota；
  确定性的 fresh workers 只负责剩余缺额。
- `_sN` 记录下一个尚未配置的 logical worker-stream ordinal。没有 replay bank 时，
  workers 使用 logical streams `0..workers-1`，保留 worker 0 使用 root seed、后续
  worker 使用 `DeriveSeed(Index=worker-1)` 的既有映射。Top-up Run 会从所有已配置
  路径中最大的可识别 `_sN` 开始；来源即使缺失、不相容或因 quota 已满而未开启，
  其文件名 cursor 仍参与计算。Fresh fan-out 一旦启动，cursor 固定增加 workers 数量。
- Stream cursor 只是 best-effort 的碰撞规避，不保证第三方 PRNG factory 一定产生
  不同状态。Collection 完成后，Optimizer 会在每个 Class 内分别按完整 snapshot
  bytes 稽核；没有重复才继续，发现同 Class duplicate 时只保存 Class-local 去重后的
  `.distinct.bin` recovery bank，并在进入 Prepare 前停止。
- `RunReport.Collection` 会记录每个来源的结束状态及
  accepted／duplicate／unmatched／rejected 计数，并按 Class 分解 replay 与 fresh
  接受数量，同时记录文件名 cursor 是否可识别、duplicate audit 及保存 bank 的
  cursor／variant。来源路径及其声明顺序会影响 configuration hash；来源 bytes 则由
  保存后 bank 的 SHA-256 独立提供证据。
- 缺失或格式错误的 bank 会产生 warning 并被跳过。若与当前 runtime 不兼容，系统
  会保留该来源中已接受的有效前缀、发出 warning，再继续处理下一个来源。所有 Class
  quota 填满后，剩余来源不会再开启。这些来源状态都不会被当成配置不可行；最终仍由
  fresh collection 判断是否能补齐所需 support。没有可识别 `_sN` 的旧文件或改名文件
  仍可 replay，但会发出 warning，并以 cursor `0` 参与 best-effort 分流。
- Collection 返回有效内部状态后，无论完整或产生 `CollectionInsufficient`，都会在
  动态验证、建立 LP、求解、materialization 与发布之前先原子保存：

  ```text
  <output.directory>/collected/game_<gid>/mode_<mode>/seed_bank_<unix_timestamp>_s<next>.bin
  ```

  若 collection 后的 audit 找到 duplicate，则只写以下 recovery artifact，并以
  `DuplicateReplayIdentity` 停止 Run：

  ```text
  <output.directory>/collected/game_<gid>/mode_<mode>/seed_bank_<unix_timestamp>_s<next>.distinct.bin
  ```

  Writer 会按规范化的 Class／sequence 顺序串流写入 snapshot，并通过
  `RunReport.Collection` 回报绝对路径、SHA-256、snapshot 长度、seed 数量、byte
  大小，以及是否为 partial／distinct bank。Duplicate warning 会在 recovery 写入前
  先显示 planned target；只有 atomic commit 成功后才会显示 `[Save distinct]`。
- Problab 不会为 Collection Bank 建立 `latest` 指针、manifest、目录索引或
  descriptor sidecar。若要在后续 Run 重放，请明确把目标 timestamp 文件路径填入
  `collected_seed`。Collection Bank 是可复用的 Optimizer 输入，不是可直接发布的
  runtime artifact。
- 在交互式终端中，每条 replay progress 会在同一行原地刷新；redirect stderr 与
  CI log 仍维持 append-only，且不会混入游标控制码。

> 说明：该模块仍处于早期阶段，接口与配置格式可能在 v1.0.0 之前演进。

### Optimal Artifact 运行方式

新的运行时配置使用一份 manifest 描述完整 Artifact：

```yaml
optimal_setting:
  use_optimal: true
  artifact: game_0/manifest.json
```

- `WithOptimalFS(fsys)`：每份 Artifact 只读入内存一次，适合 embed、示例与可携工具。
- `WithOptimalDir(root)`：在支持的 Unix 平台对 probability、alias、seed bank
  二进制文件进行只读 mmap。
- 同一个 Problab 建立的 Machine、Simulator worker、MachinePool 全部共享同一份
  不可变 Artifact。
- 应用停止时先关闭 Runtime，再调用 `Problab.Close()`。

旧的 `gachas`／`seed_bank` 配置仍可作为迁移兼容格式读取，但只能使用内存后端。

> 说明：该模块仍处于早期阶段，接口与配置格式可能在 v1.0.0 之前演进。

---

## 快速开始

**1分钟成功执行，3分钟生产环境就绪！**

本仓库专注于引擎本身，实际项目请从 scaffold 开始

使用 **problab-scaffold** — 基于 Problab 的干净起步模板。

👉 https://github.com/zintix-labs/problab-scaffold

模板提供：
- 预配置的配置 / 逻辑 / 服务器 / 模拟
- 一条命令启动（`make run`、`make dev`、`make svr`）
- 适合私有商业开发的项目结构

这是使用 Problab 构建真实游戏的**推荐方式**。

---

## 确定性与可复现性

- 基于种子的执行
- 可重放的结果
- 模拟与服务器行为一致
- 默认使用 ChaCha20，并保留显式 PCG64 兼容模式

这使得 Problab 适用于：

- 数学审计
- 回归测试
- 生产问题调查

自定义 PRNG Factory 现在接收不透明的 `[]byte` seed，并负责确定性的串流派生。
既有公开 `int64` seed API 仍然保留；需要旧 PCG 输出或 optimizer artifact
逐位兼容时，请显式使用 `core.PCG64()`。Production 新游戏周期会调用 PRNG
强制实现的 `Reseed` hook；显式 seed、Simulator、Optimizer 与 replay 路径仍保持
确定性。本地设计说明与迁移记录不包含在发布的 module 中。

---

## 状态

- API 可能会在v1.0.0之前演进
- 重点在正确性、性能和核心架构

文档、测试和起步模板将逐步完善。

---

## 路线图

- Jackpot 支持（配置 + 快照/增量 delta）
- 更多可复用的 ops 操作
- （RFC）用于状态转换的 Trigger methods

---

## 贡献

对于 v0.x.y，我们**仅接受**：

- Bug 报告（尽可能附带最小复现或日志）
- 文档改进（修正、示例、翻译）

功能需求欢迎讨论，但可能暂不优先处理。

---

## 许可证

Apache License 2.0  
详见 [LICENSE](LICENSE) 和 [NOTICE](NOTICE)。
