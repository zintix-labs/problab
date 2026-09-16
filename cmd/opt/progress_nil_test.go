package main

// Regression from independent review: all final-report paths honor nil output.

import (
	"testing"

	optimizerv2 "github.com/zintix-labs/problab/optimizer/v2"
)

func TestReportNilWriter(t *testing.T) {
	for _, c := range []struct {
		name   string
		result optimizerv2.RunResult
	}{
		{"exported", optimizerv2.RunResult{Status: optimizerv2.StatusExported}},
		{"rgs-exports", optimizerv2.RunResult{Status: optimizerv2.StatusOptimal,
			Report: optimizerv2.RunReport{RGSExports: []optimizerv2.RGSExportReport{{Family: "collected", State: "COMPLETED"}}}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("reportV2Outcome(nil, ...) panicked: %v", r)
				}
			}()
			reportV2Outcome(nil, c.result)
		})
	}
}
