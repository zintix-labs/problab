package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-chi/chi/v5"
	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/demo/demo_configs"
	"github.com/zintix-labs/problab/demo/demo_logic"
	"github.com/zintix-labs/problab/sdk/buf"
	"github.com/zintix-labs/problab/sdk/core"
	"github.com/zintix-labs/problab/sdk/slot"
	"github.com/zintix-labs/problab/server/netsvr"
	"github.com/zintix-labs/problab/server/svrcfg"
	"github.com/zintix-labs/problab/stats"
)

// testSvr 以 chi 實作 NetSvr，讓測試取得掛上實際 middleware 與路由的 http.Handler。
type testSvr struct{ r chi.Router }

func (s *testSvr) Use(mw func(http.Handler) http.Handler) { s.r.Use(mw) }
func (s *testSvr) Get(p string, h http.HandlerFunc)       { s.r.Get(p, h) }
func (s *testSvr) Post(p string, h http.HandlerFunc)      { s.r.Post(p, h) }
func (s *testSvr) Put(p string, h http.HandlerFunc)       { s.r.Put(p, h) }
func (s *testSvr) Delete(p string, h http.HandlerFunc)    { s.r.Delete(p, h) }
func (s *testSvr) Group(p string, fn func(netsvr.NetRouter)) {
	s.r.Route(p, func(r chi.Router) { fn(&testSvr{r: r}) })
}
func (s *testSvr) Run() error                     { return nil }
func (s *testSvr) Shutdown(context.Context) error { return nil }

func newTestServer(t *testing.T, lab *problab.Problab) *httptest.Server {
	t.Helper()
	svr := &testSvr{r: chi.NewRouter()}
	cfg := &svrcfg.SvrCfg{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), SlotBufSize: 1, Problab: lab, Mode: svrcfg.ModeDev}
	if err := RegisterRoutes(svr, cfg); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(svr.r)
	t.Cleanup(ts.Close)
	return ts
}

func choiceLab(t *testing.T) *problab.Problab {
	t.Helper()
	lab, err := problab.NewAuto(core.Default(), problab.Configs(demo_configs.FS, demo_configs.StagedFS), problab.Logics(demo_logic.Logics))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	return lab
}

func call(t *testing.T, ts *httptest.Server, method, path string, body any) (int, []byte) {
	t.Helper()
	return callWith(t, ts, ts.Client(), method, path, body)
}

func callWith(t *testing.T, ts *httptest.Server, client *http.Client, method, path string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, ts.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, raw
}

// game0 是 dev API 的遊戲參數：gid 0 視為未指定，所以以名稱指定 game 0。
func game0(t *testing.T, lab *problab.Problab) string {
	t.Helper()
	ent, ok := lab.EntryById(0)
	if !ok {
		t.Fatal("no game 0")
	}
	return ent.Name
}

func requireStatus(t *testing.T, step string, code int, raw []byte, want int, contains string) {
	t.Helper()
	if code != want || !strings.Contains(string(raw), contains) {
		t.Fatalf("%s: status %d body %s; want %d containing %q", step, code, raw, want, contains)
	}
}

func canonical(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var a any
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(a)
	return string(out)
}

// choose 只接受 first、last 或省略；其他值以 400 拒絕。
func TestChooseParameterValidation(t *testing.T) {
	lab := choiceLab(t)
	ts := newTestServer(t, lab)
	name := game0(t, lab)
	for _, bad := range []string{"player", "First", "auto"} {
		code, raw := call(t, ts, http.MethodGet, "/v1/sim?gid=0&bet_mode=0&round=10&seed=1&choose="+bad, nil)
		requireStatus(t, "GET /v1/sim "+bad, code, raw, http.StatusBadRequest, "choose must be first or last")
		code, raw = call(t, ts, http.MethodPost, "/v1/simplayer", map[string]any{"gid": 0, "player": 2, "bets": 50, "bet_mode": 0, "round": 10, "seed": 1, "choose": bad})
		requireStatus(t, "POST /v1/simplayer "+bad, code, raw, http.StatusBadRequest, "choose must be first or last")
		code, raw = call(t, ts, http.MethodPost, "/dev/spin", map[string]any{"game": name, "betmode": 0, "rounds": 5, "seed": "1", "choose": bad})
		requireStatus(t, "POST /dev/spin "+bad, code, raw, http.StatusBadRequest, "choose must be first or last")
		code, raw = call(t, ts, http.MethodPost, "/dev/sim", map[string]any{"game": name, "betmode": 0, "rounds": 5, "seed": "1", "choose": bad})
		requireStatus(t, "POST /dev/sim "+bad, code, raw, http.StatusBadRequest, "choose must be first or last")
	}
}

// 省略 choose 時，deterministic 處理器的報表等於以相同 seed 直接呼叫 Go API。
func TestRoutesWithoutChooseMatchGoAPI(t *testing.T) {
	lab := choiceLab(t)
	ts := newTestServer(t, lab)
	sim, err := lab.NewSimulator(0, problab.MachineConfig{Seed: problab.SeedInt64(7)})
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := sim.Sim(0, 2000, false)
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Stats *stats.StatReport `json:"stats"`
	}
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/sim?gid=0&bet_mode=0&round=2000&seed=7", nil},
		{http.MethodPost, "/v1/sim", map[string]any{"gid": 0, "bet_mode": 0, "round": 2000, "seed": 7}},
	} {
		code, raw := call(t, ts, c.method, c.path, c.body)
		if code != http.StatusOK || json.Unmarshal(raw, &resp) != nil || canonical(t, resp.Stats) != canonical(t, want) {
			t.Fatalf("%s %s: status %d body %.200s", c.method, c.path, code, raw)
		}
	}

	d, err := lab.NewDevSimulator(0, 7)
	if err != nil {
		t.Fatal(err)
	}
	spins, err := d.Spins(0, 300)
	if err != nil {
		t.Fatal(err)
	}
	code, raw := call(t, ts, http.MethodPost, "/dev/spin", map[string]any{"game": game0(t, lab), "betmode": 0, "rounds": 300, "seed": "7"})
	var gotSpins problab.DevSpinReport
	if code != http.StatusOK || json.Unmarshal(raw, &gotSpins) != nil || canonical(t, gotSpins) != canonical(t, spins) {
		t.Fatalf("/dev/spin: status %d", code)
	}
	d, err = lab.NewDevSimulator(0, 7)
	if err != nil {
		t.Fatal(err)
	}
	dsim, err := d.Sim(0, 2000)
	if err != nil {
		t.Fatal(err)
	}
	code, raw = call(t, ts, http.MethodPost, "/dev/sim", map[string]any{"game": game0(t, lab), "betmode": 0, "rounds": 2000, "seed": "7"})
	var gotSim problab.DevSimReport
	if code != http.StatusOK || json.Unmarshal(raw, &gotSim) != nil || canonical(t, gotSim) != canonical(t, dsim) {
		t.Fatalf("/dev/sim: status %d", code)
	}
}

// v1 SimPlayers 固定 4 個 worker，依排程分派玩家：只驗證不變量。
func TestSimPlayersRouteInvariants(t *testing.T) {
	ts := newTestServer(t, choiceLab(t))
	code, raw := call(t, ts, http.MethodPost, "/v1/simplayer", map[string]any{"gid": 0, "player": 20, "bets": 100, "bet_mode": 0, "round": 100, "seed": 3})
	var resp struct {
		Stats *stats.StatReport       `json:"stats"`
		Est   *stats.EstimatorPlayers `json:"est"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &resp) != nil || resp.Stats == nil || resp.Est == nil {
		t.Fatalf("status %d body %.200s", code, raw)
	}
	sum := resp.Stats.Summary
	if sum.Rounds < 1 || sum.Rounds > 20*100 || sum.TotalBet != sum.Rounds*sum.BetUnit || sum.Choose != "" {
		t.Fatalf("summary %+v", sum)
	}
}

// 選擇類遊戲：未指定 choose 時回 400，server 繼續服務；指定 first 時正常執行並標示。
func TestChoiceGameRoutes(t *testing.T) {
	ts := newTestServer(t, choiceLab(t))
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/sim?gid=2&bet_mode=0&round=3000&seed=5", nil},
		{http.MethodPost, "/v1/simplayer", map[string]any{"gid": 2, "player": 3000, "bets": 100, "bet_mode": 0, "round": 50, "seed": 5}},
		{http.MethodPost, "/dev/spin", map[string]any{"gid": 2, "betmode": 0, "rounds": 3000, "seed": "5"}},
		{http.MethodPost, "/dev/sim", map[string]any{"gid": 2, "betmode": 0, "rounds": 3000, "seed": "5"}},
	} {
		code, raw := call(t, ts, c.method, c.path, c.body)
		requireStatus(t, c.path, code, raw, http.StatusBadRequest, "choose mode first or last")
		code, raw = call(t, ts, http.MethodGet, "/v1/health", nil)
		requireStatus(t, c.path+" then health", code, raw, http.StatusOK, "")
	}
	code, raw := call(t, ts, http.MethodGet, "/v1/sim?gid=2&bet_mode=0&round=3000&seed=5&choose=first", nil)
	requireStatus(t, "first", code, raw, http.StatusOK, `"Choose":"first"`)
	code, raw = call(t, ts, http.MethodPost, "/v1/simplayer", map[string]any{"gid": 2, "player": 10, "bets": 100, "bet_mode": 0, "round": 50, "seed": 5, "choose": "last"})
	requireStatus(t, "last players", code, raw, http.StatusOK, `"Choose":"last"`)
	code, raw = call(t, ts, http.MethodPost, "/dev/spin", map[string]any{"gid": 2, "betmode": 0, "rounds": 500, "seed": "5", "choose": "first"})
	requireStatus(t, "dev first", code, raw, http.StatusOK, `"choose":"first"`)
}

// incompleteLogic 包住 demo_normal：結果未結束卻沒有選項，屬於輸出違約。
type incompleteLogic struct{ inner slot.GameLogic }

func (l *incompleteLogic) GetResult(r *buf.SpinRequest, g *slot.Game) *buf.SpinResult {
	sr := l.inner.GetResult(r, g)
	sr.IsGameEnd = false
	return sr
}

// DevSim 遇到輸出違約時 panic，由 Recover middleware 轉成錯誤回應，server 能處理下一個請求。
func TestDevSpinOutputViolationIsRecovered(t *testing.T) {
	reg := slot.NewLogicRegistry()
	if err := reg.Register("demo_normal", func(g *slot.Game) (slot.GameLogic, error) {
		inner, err := demo_logic.Logics.Build("demo_normal", g)
		return &incompleteLogic{inner: inner}, err
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := fs.ReadFile(demo_configs.FS, "game_0_demonormal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	lab, err := problab.NewAuto(core.Default(), problab.Configs(fstest.MapFS{"game.yaml": &fstest.MapFile{Data: raw}}), problab.Logics(reg))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lab.Close() })
	ts := newTestServer(t, lab)
	plain := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	code, body := callWith(t, ts, plain, http.MethodPost, "/dev/spin", map[string]any{"game": game0(t, lab), "betmode": 0, "rounds": 5, "seed": "1"})
	if code != http.StatusInternalServerError {
		t.Fatalf("without compression: status %d body %s", code, body)
	}
	code, body = call(t, ts, http.MethodPost, "/dev/spin", map[string]any{"game": game0(t, lab), "betmode": 0, "rounds": 5, "seed": "1"})
	if code != http.StatusInternalServerError {
		t.Fatalf("with gzip: status %d body %q", code, body)
	}
	code, body = call(t, ts, http.MethodGet, "/dev/meta", nil)
	requireStatus(t, "next request", code, body, http.StatusOK, "")
}
