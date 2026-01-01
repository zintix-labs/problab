🌐 Language: En | [中文](README_ch.md)

---

# Problab

<sub>Maintained by <b>Zintix Labs</b> — <a href="https://github.com/nextso">@nextso</a></sub>

**Problab** is a high-performance slot math engine for math designers and engineers.

Build once, then **simulate, reproduce, and ship to production**  
with the **same source of truth**.

**Simulation runs the same execution path as production.**

---

## What is Problab?

**If it runs in simulation,**  
**it runs the same way in production.**

Problab is a **slot math execution engine** designed for both
large-scale simulation and real production use.


It is the **same engine** that can be used for:

- large-scale math simulation
- deterministic reproduction (seed-based)
- backend game server execution
- development & debugging

There is **no separate “simulation logic” and “production logic”**.

---

## Why Problab?

Slot math engines usually fail in one of these ways:

- Simulators are fast but **not production-ready**
- Production engines are correct but **too slow to simulate**
- Simulation logic and server logic **diverge over time**
- Reproducing a real production issue is painful or impossible

Problab is designed to solve this exact problem.

---

## Core Design Goals

- **Single Source of Truth**  
  One engine, one logic path, one result.

- **High Performance by Design**  
  Zero-allocation hot paths, cache-friendly data layout.

- **Explicit Dependency Injection**  
  No hidden globals, no magic init side effects.

- **Developer-Friendly**  
  Add a new game by providing:
  - one config file
  - one logic file

- **Same execution path in simulation and production**  
  Simulation is not a mock or a reimplementation.  
  It runs the same deterministic logic path as production.

---

## Performance Snapshot (MacBook Air M3)

All numbers below are **real measurements**, not synthetic benchmarks.

### Single Core

| Game Type | Throughput |
|----------|------------|
| Simple (5x3, 15 lines) | ~7.0M spins/sec |
| Cascade / Cluster | ~1.7M spins/sec |

### 4-Core Parallel Execution

| Game Type | Throughput |
|----------|------------|
| Simple | ~19M spins/sec |
| Cascade / Cluster | ~5.8M spins/sec |

### Real Run Output (Example)

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

> These numbers are achieved with real game logic  
> (line wins, free games, cascades, gravity, multipliers, etc.)

---

## Typical Use Cases

- Slot math simulation & validation
- RTP / volatility analysis
- Deterministic replay & debugging
- Backend game server execution
- CI regression testing for math changes

---

## Built-in Math Reports (Stats & Verification)

Problab ships with first-class **math verification reports** for validation and regression.

Out of the box you can generate:

- **RTP + 95% CI**
- **STD / CV (volatility)**
- **Hit / No-Win / Trigger rates**
- **Win distribution buckets** (base / free / total)
- **Recorder-style summaries** for audit/debug workflows

This is designed for practical math workflows:  
**validate → compare → regress → explain** with reproducible inputs.

Problab treats **verification output** as a first-class product feature, not an afterthought.

**Example Output**

```
Total RTP: 95.56%
RTP 95% CI: [95.42%, 95.69%]
STD: 6.797
CV:  7.113
...
```

---

## Quick Start 


**Successful execution in 1 minute; production environment ready in 3 minutes.**

This repository focuses on the engine itself.
For building real games, start with the scaffold.

Use **problab-scaffold** — a clean starter template built on top of Problab.

👉 https://github.com/zintix-labs/problab-scaffold

The scaffold provides:
- pre-wired configs / logic / server / simulation
- one-command run (`make run`, `make dev`, `make svr`)
- a structure ready for private commercial development

This is the **recommended way** to build real games with Problab.

---

## Determinism & Reproducibility

- Seed-driven execution
- Replayable results
- Identical behavior between simulation and server

This makes Problab suitable for:

- math audits
- regression testing
- production issue investigation

---

## Status

- **Current version:** v0.2.0
- APIs may evolve
- Focus is on correctness, performance, and core architecture

Documentation, tests, and starter templates will be expanded iteratively.

---

## Contributing

For v0.x.y, we **only accept**:

- Bug reports (with minimal repro / logs if possible)
- Documentation improvements (fixes, examples, translations)

Feature requests are welcome for discussion, but may not be prioritized yet.

---

## License

Apache License 2.0  
See [LICENSE](LICENSE) and [NOTICE](NOTICE).
