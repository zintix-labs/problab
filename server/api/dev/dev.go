// Package dev 提供 ProbLab 的「內部 Dev Panel」HTTP endpoints。
//
// 目的（ explain the why ）：
//   - 給數學家 / 後端在開發期快速驗證：指定 Slot Game、Bet Mode、Seed / Snap，然後執行 Spin 或 Sim。
//   - 支援可回放（replay）：把 Snapshot（Snap）以字串形式在前端顯示，並可貼回後端做 Restore。
//
// 注意（ contract ）：
//   - 這不是 production API；它偏向 debug / tooling，行為允許更寬鬆，但仍需維持 deterministic concludes。
//   - 這裡的錯誤處理走 `httperr.Errs`（以 errs.Warn/errs.Fatal 對應 HTTP response）。
//   - Seed/Snap 的互斥與優先級由前端 + 後端共同保證（Snap takes precedence）。
package dev

import (
	"crypto/rand"
	"embed"
	"encoding/json"
	"math"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	"github.com/zintix-labs/problab"
	"github.com/zintix-labs/problab/catalog"
	"github.com/zintix-labs/problab/errs"
	"github.com/zintix-labs/problab/server/httperr"
	"github.com/zintix-labs/problab/server/netsvr"
	"github.com/zintix-labs/problab/server/svrcfg"
	"github.com/zintix-labs/problab/spec"
)

// devRequest 是 Dev Panel 的「輸入 payload」。
//
// 兼容性（backward compatibility）：
//   - 同時保留 `bet_unit` 與舊欄位 `betunit`（BetUnitAlt）。
//   - 同時保留 `rounds` 與舊欄位 `round`。
//   - `gid` 與 `game` 兩者擇一即可；若兩者同時存在，後端會優先使用 gid 做解析。
//
// Seed / Snap：
//   - Seed（int64 string）用於 deterministic 起始；若為空字串則自動生成（crypto/rand）。
//   - Snap（base64url string）代表 core snapshot；若提供 Snap，則後端以 Snap Restore 為準（Snap precedence）。
//
// 注意：
//   - 這個 struct 是 API 邊界用的 DTO；不要把它滲透到 slot logic / math domain。
type devRequest struct {
	GID        int64  `json:"gid"`
	Game       string `json:"game"`
	BetUnit    *int   `json:"bet_unit"`
	BetUnitAlt *int   `json:"betunit"`
	BetMode    *int   `json:"betmode"`
	Rounds     int    `json:"rounds"`
	Round      int    `json:"round"`
	Seed       string `json:"seed"`
	Snap       string `json:"snap"`
}

// round() 將 rounds/round 做兼容合併：優先 rounds，其次 round；若都未提供則回 0。
func (r devRequest) round() int {
	if r.Rounds > 0 {
		return r.Rounds
	}
	if r.Round > 0 {
		return r.Round
	}
	return 0
}

// betUnit() 將 bet_unit/betunit 做兼容合併：優先 bet_unit，其次 betunit
func (r devRequest) betUnit() (int, bool) {
	if r.BetUnit != nil {
		return *r.BetUnit, true
	}
	if r.BetUnitAlt != nil {
		return *r.BetUnitAlt, true
	}
	return 0, false
}

// Register 註冊 Dev Panel 的 routes。
//
// Routes：
//   - GET  /dev       ：Dev Panel HTML（內嵌 JS）。
//   - GET  /dev/meta  ：回傳 Catalog summary（供前端下拉選單：Slot Game / Bet Mode）。
//   - POST /dev/spin  ：執行 N 次 Spin 並回傳每回合結果（含 snap_before/snap_after）。
//   - POST /dev/sim   ：執行 N 次 Sim 並回傳統計報表（不回傳逐回合 results）。
//
// 依賴（dependency）：
//   - 需要 cfg.Problab 已被上層組裝完成並注入；否則會回 errs.Fatal。
func Register(svr netsvr.NetRouter, cfg *svrcfg.SvrCfg) {
	svr.Get("/dev", devPage)
	svr.Get("/favicon.svg", favicon)
	svr.Get("/dev/meta", devMeta(cfg))
	svr.Post("/dev/spin", devSpin(cfg))
	svr.Post("/dev/sim", devSim(cfg))
}

// devPageHTML 是內嵌的 Dev Panel UI。
//
// UI 行為（contract）：
//   - Slot Game：由 /dev/meta 動態載入。
//   - Seed/Snap 互斥：
//   - Snap 非空 → Seed 會被清空並 disable。
//   - Seed 非空 → Snap 會被清空並 disable。
//   - Snap takes precedence（後端也會以 Snap 為準）。
//   - Rounds：
//   - Spin：前端會 cap 在 5,000 以避免回傳 payload 過大。
//   - Sim ：前端會 cap 在 3,000,000 以避免長時間阻塞（仍屬 dev tooling）。
//
// 回傳呈現：
//   - Spin：Summary 區顯示整體統計；Spin Results 展開後可點選查看 raw DevSpinResult JSON。
//   - Sim ：僅顯示統計（statistic/stats/stat），不顯示逐回合 results。
const devPageHTML = `<!doctype html>
<html lang="zh-Hant">
<head>
  <meta charset="utf-8" />
  <link rel="icon" type="image/svg+xml" href="/favicon.svg" />
  <title>ProbLab Dev</title>
  <style>
    body { font-family: -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; background:#0f172a; color:#e2e8f0; margin:0; }
    .wrap { max-width: 980px; margin: 24px auto; padding: 16px 20px; background:#111827; border:1px solid #1f2937; border-radius:12px; box-shadow:0 12px 50px rgba(0,0,0,0.35); }
    h1 { margin: 0 0 16px; font-size: 22px; letter-spacing: 0.3px; }
    .grid { display:grid; grid-template-columns: repeat(auto-fit, minmax(180px,1fr)); gap:12px; margin-bottom:12px; }
    label { display:flex; flex-direction:column; gap:6px; font-size: 13px; color:#cbd5e1; }
    input, select { background:#0b1224; color:#e2e8f0; border:1px solid #1f2738; border-radius:8px; padding:10px 12px; font-size:14px; }
    input:focus, select:focus { outline:1px solid #38bdf8; border-color:#38bdf8; }
    .actions { position:relative; display:flex; gap:10px; align-items:center; justify-content:flex-end; margin: 8px 0 14px; }
    button { cursor:pointer; border:none; border-radius:10px; padding:10px 14px; font-weight:600; letter-spacing:0.2px; }
    #btn-spin { background:#38bdf8; color:#0b1224; }
    #btn-sim { background:#22c55e; color:#0b1224; }
    #btn-clear { background:#1f2937; color:#e2e8f0; border:1px solid #334155; }
    button:disabled { opacity:0.6; cursor:not-allowed; }
    input:disabled, select:disabled {
      opacity: 0.55;
      cursor: not-allowed;
      filter: grayscale(0.25);
    }
    label.is-disabled { opacity: 0.55; }
    label.is-disabled input, label.is-disabled select { pointer-events: none; }
    .hint { font-size: 12px; color:#94a3b8; margin-top:4px; }
    .info { position:absolute; left:50%; transform:translateX(-50%); font-size:13px; color:#94a3b8; }
    .info.warn { color:#f87171; font-weight:600; }
    #summary { background:#0b1224; border:1px solid #1f2738; border-radius:12px; padding:14px; min-height:120px; overflow:auto; font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace; white-space:pre-wrap; margin-bottom:12px; }
    #roundsBox { border:1px solid #1f2737; border-radius:12px; padding:10px; background:#0b1224; margin-bottom:12px; max-height: calc(60vh - 56px); overflow:auto; }
    #roundList { max-height: calc(60vh - 136px); overflow:auto; }
    .round-item { display:grid; grid-template-columns: minmax(3.5em, max-content) minmax(100px, max-content) max-content; align-items:center; column-gap:8px; width:100%; text-align:left; background:none; border:none; padding:6px 10px; color:#e2e8f0; font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace; cursor:pointer; border-left: 4px solid transparent; }
    .round-item:hover { background:#1f2937; border-left-color:#38bdf8; }
    .round-item.selected { background:#2563eb; border-left-color:#60a5fa; }
    .round-index { color:#94a3b8; text-align:right; justify-self:end; min-width:3.5em; font-variant-numeric: tabular-nums; }
    .round-win { text-align:right; justify-self:end; font-variant-numeric: tabular-nums; }
    .round-win.zero { color:#94a3b8; }
    .round-feature { text-align:right; justify-self:end; }
    .fg-true { color:#22c55e; font-weight:600; }
    #detail { background:#0b1224; border:1px solid #1f2738; border-radius:12px; padding:14px; min-height:220px; overflow:auto; font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace; white-space:pre-wrap; display:none; }
    .note { font-size:12px; color:#94a3b8; margin-top:4px; }
  </style>
</head>
<body>
  <div class="wrap">
    <h1>ProbLab Dev Panel</h1>
    <div class="grid">
      <label>Slot Game
        <select id="game"></select>
      </label>
      <label>Seed (int64)
   <input id="seed" type="text" inputmode="numeric" placeholder="Empty = auto" />
      </label>
      <label>Snap(base64url)
        <input id="snap" type="text" placeholder="Paste snap (base64url)" />
      </label>
      <label>Bet Mode
        <select id="betunit"></select>
      </label>
      <label>Rounds
        <input id="rounds" type="number" min="1" max="3000000" value="1" />
      </label>
    </div>
    <div class="actions">
      <button id="btn-spin">Spin</button>
      <button id="btn-sim">Sim</button>
      <button id="btn-clear">Clear</button>
      <span class="info" id="info"></span>
    </div>

    <pre id="summary"></pre>

    <details id="roundsBox" style="display:none;">
      <summary>Spin Results</summary>
      <div id="roundList"></div>
    </details>

    <pre id="detail" style="display:none;"></pre>
  </div>
<script>
const state = { meta: null, results: [] };
const gameSel = document.getElementById('game');
const betUnitSel = document.getElementById('betunit');
const seedInput = document.getElementById('seed');
const snapInput = document.getElementById('snap');
const roundsInput = document.getElementById('rounds');
const summary = document.getElementById('summary');
const roundsBox = document.getElementById('roundsBox');
const roundList = document.getElementById('roundList');
const detail = document.getElementById('detail');
const infoEl = document.getElementById('info');
const btnRun = document.getElementById('btn-spin');
const btnSim = document.getElementById('btn-sim');
const btnClear = document.getElementById('btn-clear');
const numberFormatter = new Intl.NumberFormat('en-US');

function setDisabled(el, disabled) {
  el.disabled = disabled;
  const label = el.closest('label');
  if (label) label.classList.toggle('is-disabled', disabled);
}

function syncInputLocks() {
  const seedValue = seedInput.value.trim();
  const snapValue = snapInput.value.trim();

  if (snapValue !== '') {
    seedInput.value = '';
    setDisabled(seedInput, true);
    setDisabled(snapInput, false);
    return;
  }
  if (seedValue !== '') {
    snapInput.value = '';
    setDisabled(snapInput, true);
    setDisabled(seedInput, false);
    return;
  }
  setDisabled(seedInput, false);
  setDisabled(snapInput, false);
}

function formatWinAmount(value) {
  if (typeof value !== 'number' || !Number.isFinite(value)) return '0';
  return numberFormatter.format(value);
}

async function loadMeta() {
  try {
    const res = await fetch('/dev/meta');
    if (!res.ok) throw new Error(await res.text());
    const data = await res.json();
    const games = Array.isArray(data) ? data : (data.games || data.summary || []);
    state.meta = { games };
    gameSel.innerHTML = '';
    state.meta.games.forEach((g) => {
      const opt = document.createElement('option');
      const gid = g.gid ?? g.id ?? g.GID;
      opt.value = gid != null ? String(gid) : (g.name || g.game || '');
      opt.textContent = g.name || g.game || String(opt.value);
      opt.dataset.name = g.name || g.game || '';
      gameSel.appendChild(opt);
    });
    refreshBetUnits();
    summary.textContent = '';
    roundsBox.style.display = 'none';
    detail.style.display = 'none';
    setInfo('', false);
  } catch (err) {
    summary.textContent = 'Failed to load meta: ' + err.message;
  }
}

function getSelectedGame() {
  if (!state.meta || !state.meta.games) return null;
  const value = gameSel.value;
  return state.meta.games.find((g) => String(g.gid ?? g.id ?? g.GID) === value)
    || state.meta.games.find((g) => (g.name || g.game || '') === value);
}

function refreshBetUnits() {
  betUnitSel.innerHTML = '';
  const gm = getSelectedGame();
  if (!gm) return;
  const betUnits = gm.bet_units || gm.betunits || gm.betUnits || [];
  betUnits.forEach((b) => {
    const opt = document.createElement('option');
    opt.value = b;
    opt.textContent = String(b);
    betUnitSel.appendChild(opt);
  });
}

gameSel.addEventListener('change', refreshBetUnits);

function setInfo(text, isWarn) {
  infoEl.textContent = text;
  if (isWarn) {
    infoEl.classList.add('warn');
  } else {
    infoEl.classList.remove('warn');
  }
}

function setLoading(isLoading) {
  btnRun.disabled = isLoading;
  btnSim.disabled = isLoading;
  if (isLoading) {
    setInfo('Running…', false);
  }
}

function clearSelection() {
  summary.textContent = '';
  roundsBox.style.display = 'none';
  detail.style.display = 'none';
  roundList.innerHTML = '';
  state.results = [];
}

function renderDetail(index) {
  if (!state.results || !state.results[index]) {
    detail.textContent = '';
    detail.style.display = 'none';
    return;
  }
  const result = state.results[index];
  detail.textContent = JSON.stringify(result, null, 2);
  detail.style.display = 'block';

  // highlight selected
  const buttons = roundList.querySelectorAll('.round-item');
  buttons.forEach((btn, idx) => {
    if (idx === index) {
      btn.classList.add('selected');
    } else {
      btn.classList.remove('selected');
    }
  });
}

async function run() {
  setLoading(true);
  clearSelection();
  const seed = seedInput.value.trim();
  const snap = snapInput.value.trim();
  const inputRounds = Number(roundsInput.value) || 1;
  const selectedGame = getSelectedGame();
  const betUnit = Number(betUnitSel.value);
  const safeRounds = Math.min(inputRounds, 5000);
  const payload = {
    bet_unit: betUnit,
    betunit: betUnit,
    rounds: safeRounds,
    round: safeRounds,
  };
  const gid = Number(gameSel.value);
  if (Number.isFinite(gid)) {
    payload.gid = gid;
  }
  if (selectedGame && selectedGame.name) {
    payload.game = selectedGame.name;
  } else if (gameSel.value) {
    payload.game = gameSel.value;
  }
  if (snap) {
    payload.snap = snap;
  } else if (seed) {
    payload.seed = seed;
  }
  try {
    const res = await fetch('/dev/spin', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
    if (!res.ok) throw new Error(await res.text());
    const data = await res.json();

    const summaryObj = { ...data };
    delete summaryObj.results;
    summary.textContent = JSON.stringify(summaryObj, null, 2);

    if (inputRounds > 5000) {
      setInfo('Run records are capped at 5,000 rounds.', true);
    } else {
      setInfo('', false);
    }

    const results = Array.isArray(data.results) ? data.results : [];
    if (results.length > 0) {
      state.results = results;
      roundList.innerHTML = '';
      results.forEach((dto, idx) => {
        const spin = dto && (dto.spin_result || dto.spinResult || dto.result || dto);
        const gmList = Array.isArray(spin && (spin.gamemodes || spin.game_modes || spin.gameModes))
          ? (spin.gamemodes || spin.game_modes || spin.gameModes)
          : [];
        const fg = gmList.length > 1;
        const win = (spin && typeof spin.win === 'number') ? spin.win
          : (spin && typeof spin.total_win === 'number') ? spin.total_win
          : 0;
        const winText = formatWinAmount(win);
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'round-item';
        btn.textContent = '';
        const idxSpan = document.createElement('span');
        idxSpan.className = 'round-index';
        idxSpan.textContent = '#' + (idx + 1);
        const winSpan = document.createElement('span');
        winSpan.className = 'round-win' + (win === 0 ? ' zero' : '');
        winSpan.textContent = winText;
        const featureSpan = document.createElement('span');
        featureSpan.className = 'round-feature';
        const fgSpan = document.createElement('span');
        fgSpan.textContent = fg ? 'feature' : '';
        if (fg) {
          fgSpan.className = 'fg-true';
        }
        featureSpan.appendChild(fgSpan);
        btn.appendChild(idxSpan);
        btn.appendChild(winSpan);
        btn.appendChild(featureSpan);
        btn.title = 'Round ' + (idx + 1) + ' | win=' + winText + (fg ? ' | feature' : '');
        btn.addEventListener('click', () => {
          renderDetail(idx);
        });
        roundList.appendChild(btn);
      });
      roundsBox.style.display = 'block';
      renderDetail(0);
    } else {
      roundsBox.style.display = 'none';
      detail.style.display = 'none';
      state.results = [];
    }
  } catch (err) {
    summary.textContent = 'Request failed: ' + err.message;
    setInfo('', false);
  } finally {
    setLoading(false);
  }
}

async function runSim() {
  setLoading(true);
  clearSelection();
  const seed = seedInput.value.trim();
  const snap = snapInput.value.trim();
  const inputRounds = Number(roundsInput.value) || 1;
  const selectedGame = getSelectedGame();
  const betUnit = Number(betUnitSel.value);
  const safeRounds = Math.min(inputRounds, 3000000);
  const payload = {
    bet_unit: betUnit,
    betunit: betUnit,
    rounds: safeRounds,
    round: safeRounds,
  };
  const gid = Number(gameSel.value);
  if (Number.isFinite(gid)) {
    payload.gid = gid;
  }
  if (selectedGame && selectedGame.name) {
    payload.game = selectedGame.name;
  } else if (gameSel.value) {
    payload.game = gameSel.value;
  }
  if (snap) {
    payload.snap = snap;
  } else if (seed) {
    payload.seed = seed;
  }
  try {
    const res = await fetch('/dev/sim', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
    if (!res.ok) throw new Error(await res.text());
    const data = await res.json();
    const summaryObj = data.statistic || data.stats || data.stat || data;
    summary.textContent = JSON.stringify(summaryObj, null, 2);
    if (inputRounds > 3000000) {
      setInfo('Sim statistics are capped at 3,000,000 rounds.', true);
    } else {
      setInfo('', false);
    }
  } catch (err) {
    summary.textContent = 'Request failed: ' + err.message;
    setInfo('', false);
  } finally {
    setLoading(false);
  }
}

btnRun.addEventListener('click', run);
btnSim.addEventListener('click', runSim);
btnClear.addEventListener('click', () => {
  clearSelection();
  setInfo('', false);
});
seedInput.addEventListener('input', syncInputLocks);
snapInput.addEventListener('input', syncInputLocks);

syncInputLocks();
loadMeta();
</script>
</body>
</html>`

// devPage 回傳內嵌 HTML（single page）。這裡不做 templating，降低 dev tool 維護成本。
func devPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(devPageHTML))
}

// favicon 提供 Dev Panel 的 favicon.svg。
func favicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Write([]byte(faviconSVG))
}

// devMeta 回傳 Catalog summary（JSON）。
//
// 前端依賴欄位：
//   - gid / id / GID
//   - name / game
//   - bet_units / betunits / betUnits
func devMeta(cfg *svrcfg.SvrCfg) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		pb, ok := getProblab(cfg)
		if !ok {
			httperr.Errs(w, errs.NewFatal("problab is required"))
			return
		}
		sum, err := pb.Summary()
		if err != nil {
			httperr.Errs(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sum)
	}
}

// devSpin 執行「可回放」的 Spin。
//
// 流程（high level）：
//  1. decode devRequest（JSON body）
//  2. resolve game（gid/name）→ catalog.Summary
//  3. resolve bet mode（by bet_unit or betmode）
//  4. resolve seed（empty = auto）
//  5. 建立 DevSimulator → Spins() 或 RestoreSpins()
//
// Snap precedence：若 snap 非空，會走 RestoreSpins(snap, ...)。
func devSpin(cfg *svrcfg.SvrCfg) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		req := new(devRequest)
		if err := json.NewDecoder(r.Body).Decode(req); err != nil {
			httperr.Errs(w, errs.NewWarn("invalid json:"+err.Error()))
			return
		}
		pb, ok := getProblab(cfg)
		if !ok {
			httperr.Errs(w, errs.NewFatal("problab is required"))
			return
		}
		sum, err := resolveSummary(pb, req)
		if err != nil {
			httperr.Errs(w, err)
			return
		}
		round := req.round()
		if round < 1 {
			httperr.Errs(w, errs.NewWarn("round is required"))
			return
		}
		betMode, err := resolveBetMode(sum, req)
		if err != nil {
			httperr.Errs(w, err)
			return
		}
		snap := strings.TrimSpace(req.Snap)
		seed, err := resolveSeed(req.Seed)
		if err != nil {
			httperr.Errs(w, err)
			return
		}
		sim, err := pb.NewDevSimulator(sum.GID, seed)
		if err != nil {
			httperr.Errs(w, err)
			return
		}
		var report problab.DevSpinReport
		if snap != "" {
			report, err = sim.RestoreSpins(snap, betMode, round)
		} else {
			report, err = sim.Spins(betMode, round)
		}
		if err != nil {
			httperr.Errs(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(report)
	}
}

// devSim 執行統計模擬（simulation）。
//
// 和 devSpin 的差異：
//   - devSim 不回逐回合 results（降低 response size），僅回 DevSimReport（statistic）。
//   - 若提供 snap，會走 RestoreSim(snap, ...)。
func devSim(cfg *svrcfg.SvrCfg) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		req := new(devRequest)
		if err := json.NewDecoder(r.Body).Decode(req); err != nil {
			httperr.Errs(w, errs.NewWarn("invalid json:"+err.Error()))
			return
		}
		pb, ok := getProblab(cfg)
		if !ok {
			httperr.Errs(w, errs.NewFatal("problab is required"))
			return
		}
		sum, err := resolveSummary(pb, req)
		if err != nil {
			httperr.Errs(w, err)
			return
		}
		round := req.round()
		if round < 1 {
			httperr.Errs(w, errs.NewWarn("round is required"))
			return
		}
		betMode, err := resolveBetMode(sum, req)
		if err != nil {
			httperr.Errs(w, err)
			return
		}
		snap := strings.TrimSpace(req.Snap)
		seed, err := resolveSeed(req.Seed)
		if err != nil {
			httperr.Errs(w, err)
			return
		}
		sim, err := pb.NewDevSimulator(sum.GID, seed)
		if err != nil {
			httperr.Errs(w, err)
			return
		}
		var report problab.DevSimReport
		if snap != "" {
			report, err = sim.RestoreSim(snap, betMode, round)
		} else {
			report, err = sim.Sim(betMode, round)
		}
		if err != nil {
			httperr.Errs(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(report)
	}
}

// getProblab 從 server config 取得已組裝的 Problab instance。
// Dev routes 不負責組裝（assembler），只負責使用（runtime entry）。
func getProblab(cfg *svrcfg.SvrCfg) (*problab.Problab, bool) {
	if cfg == nil || cfg.Problab == nil {
		return nil, false
	}
	return cfg.Problab, true
}

// resolveSummary 解析使用者指定的遊戲：
//   - 若 gid > 0：以 gid 精準匹配（fast path）。
//   - 否則若 game(name) 非空：先做 case-insensitive name 匹配；若 remember 也允許把 game 當作數字字串解析成 gid。
//
// 回傳 catalog.Summary 作為後續 Bet Mode / bet mode 的依據。
func resolveSummary(pb *problab.Problab, req *devRequest) (catalog.Summary, error) {
	sums, err := pb.Summary()
	if err != nil {
		return catalog.Summary{}, err
	}
	if req.GID > 0 {
		gid := spec.GID(req.GID)
		for _, s := range sums {
			if s.GID == gid {
				return s, nil
			}
		}
		return catalog.Summary{}, errs.NewWarn("gid not found")
	}
	name := strings.TrimSpace(req.Game)
	if name != "" {
		for _, s := range sums {
			if strings.EqualFold(s.Name, name) {
				return s, nil
			}
		}
		if gid, err := strconv.ParseUint(name, 10, 64); err == nil {
			sg := spec.GID(gid)
			for _, s := range sums {
				if s.GID == sg {
					return s, nil
				}
			}
		}
		return catalog.Summary{}, errs.NewWarn("game not found")
	}
	return catalog.Summary{}, errs.NewWarn("game is required")
}

// resolveBetMode 決定要用哪個 bet mode：
//   - 若提供 Bet Mode：用 summary.BetUnits 做查表，回傳對應 index（mode）。
//   - 否則若提供 betmode：檢查範圍後直接使用。
//
// 設計理由：Dev Panel 對人類更友善的是 Bet Mode；但仍保留 betmode 給進階使用者。
func resolveBetMode(sum catalog.Summary, req *devRequest) (int, error) {
	if bu, ok := req.betUnit(); ok {
		for i, unit := range sum.BetUnits {
			if unit == bu {
				return i, nil
			}
		}
		return 0, errs.NewWarn("Bet Mode not found")
	}
	if req.BetMode != nil {
		bm := *req.BetMode
		if bm < 0 || bm >= len(sum.BetUnits) {
			return 0, errs.NewWarn("bet mode out of range")
		}
		return bm, nil
	}
	return 0, errs.NewWarn("Bet Mode is required")
}

// resolveSeed 解析 seed（int64 string）。
//   - 空字串：自動生成 seed（crypto/rand），方便快速測試。
//   - 非空：必須為合法 int64。
func resolveSeed(seed string) (int64, error) {
	seed = strings.TrimSpace(seed)
	if seed == "" {
		return randomSeed()
	}
	v, err := strconv.ParseInt(seed, 10, 64)
	if err != nil {
		return 0, errs.NewWarn("seed must be int64")
	}
	return v, nil
}

// randomSeed 使用 crypto/rand 產生 [0, MaxInt64) 的種子。
// 目的：避免 math/rand 的 deterministic 來源造成 seed 品質偏差（dev tool 也要可依賴）。
func randomSeed() (int64, error) {
	rnd, err := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
	if err != nil {
		return 0, errs.NewWarn("seed generate failed")
	}
	return rnd.Int64(), nil
}

//go:embed favicon.svg
var faviconSVG string

// keep embed imported even if only used for directives
var _ embed.FS
