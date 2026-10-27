// Copyright 2026 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v1

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stat API 自己組出 Recorder：報表帶有 Welford 算出的 Std、BaseCv、FreeCv，不再有平方和。
func TestStatReportsStdAndCv(t *testing.T) {
	body, err := json.Marshal(DistStat{
		GameName: "g", BetUnits: []int{40}, Bet: 40, BetMult: 1,
		TotalWins: []int{40, 120, 0, 400, 80},
		BaseWins:  []int{40, 40, 0, 0, 80},
		FreeWins:  []int{0, 80, 0, 400, 0},
		Triggers:  []int{0, 1, 0, 1, 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	Stat(w, httptest.NewRequest(http.MethodPost, "/stat", bytes.NewReader(body)))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "SqSum") {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	var rep struct {
		Summary struct{ Std, Cv, BaseCv, FreeCv, RTP float64 }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	sample := func(xs ...float64) (std, mean float64) {
		for _, x := range xs {
			mean += x
		}
		mean /= float64(len(xs))
		for _, x := range xs {
			std += (x - mean) * (x - mean)
		}
		return math.Sqrt(std / float64(len(xs)-1)), mean
	}
	std, _ := sample(1, 3, 0, 10, 2)
	baseStd, baseMean := sample(1, 1, 0, 0, 2)
	freeStd, freeMean := sample(2, 10) // 只有觸發的兩局
	for name, c := range map[string][2]float64{
		"std": {rep.Summary.Std, std}, "cv": {rep.Summary.Cv, std / rep.Summary.RTP},
		"base cv": {rep.Summary.BaseCv, baseStd / baseMean}, "free cv": {rep.Summary.FreeCv, freeStd / freeMean},
	} {
		if math.Abs(c[0]-c[1]) > 1e-12 {
			t.Errorf("%s %v, want %v", name, c[0], c[1])
		}
	}
}
