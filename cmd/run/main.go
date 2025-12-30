package main

import "github.com/zintix-labs/problab/sdk/perf"

// makefile runner
func main() {
	bindVar()
	perf.RunPProf(executeSimulator, cfg.pprofmode)
}
