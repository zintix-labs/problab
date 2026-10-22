# Contributing

Thank you for your interest in Problab.

At this stage, the project accepts contributions in the following areas only:

- Bug reports
- Documentation improvements
- Typo fixes and clarifications

The following are intentionally out of scope for now:

- New features
- API or behavior changes
- Game logic or math design proposals

This project is currently focused on establishing a stable, production-grade
core and execution model. Broader contribution scopes may be considered in
future releases.

Thank you for your understanding.

## Test layout

Tests in a package live next to the code they test, so the root package keeps
its test files in the repository root. To keep them easy to find, name test
files after the source file they cover, not after the change that added them:

| Test file | Covers |
| --- | --- |
| `construction_test.go` | `construction.go` |
| `machine_test.go` | `machine.go` (entry-point contracts, requests seen by games) |
| `machine_exec_test.go` | `machine_exec.go` (RNG start point and fault injection) |
| `problab_test.go` | `problab.go` (optimal artifact loading and compatibility) |
| `prng_test.go` | PRNG lifecycle across Machine, pool and runtime |
| `bench_test.go` | benchmarks |
| `helpers_test.go` | fixtures and helpers shared by several test files |
| `api_external_test.go` | black-box tests of the public API (`package problab_test`) |

- Add a new test to the file of the source file it covers; create
  `<source>_test.go` when none exists yet (for example `sim_test.go`).
- Put a helper in `helpers_test.go` once a second test file needs it.
- Temporary acceptance tooling (such as a recorded baseline) may use its own
  clearly named file, and is removed once the change is accepted.
