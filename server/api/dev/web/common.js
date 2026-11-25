// ProbLab Dev 公版：機台與押注選擇、局列表、盤面、逐步播放、中獎明細、player 選擇與回放。玩家統計在 stats.js。
// 機台專屬的畫法放在 games/<gid>.js，透過 DEV.register(gid, plugin) 掛上來，介面見 README.md。
"use strict";

const DEV = (window.DEV = {
  plugins: {},
  // 目前的介面語言（"en" 或 "zh"）；外掛的文字用 DEV.t({ en, zh }) 依語言取，切換語言時公版會重畫外掛的部分。
  // title、modes 的值與 panel、badge、label、actLabel 的回傳值也可以直接是 { en, zh }，公版會依語言取
  get lang() {
    return i18n.lang;
  },
  t: pickLang,
  register(gid, plugin) {
    this.plugins[gid] = plugin;
    if (plugin.css) {
      const style = document.createElement("style");
      style.textContent = plugin.css;
      document.head.appendChild(style);
    }
    if (state.game && state.game.gid === gid) render();
  },
  util: {
    esc,
    // kv([["標記圖標", "H1"], ...]) → 兩欄表
    kv(rows) {
      return `<dl class="kv">${rows.map(([k, v]) => `<dt>${esc(k)}</dt><dd>${v}</dd>`).join("")}</dl>`;
    },
    // progress(3, 10, "能量") → 進度條＋數字
    progress(value, total, label = "") {
      const pct = total > 0 ? Math.max(0, Math.min(100, (value / total) * 100)) : 0;
      return `<div>${esc(label)} <span class="muted">${value} / ${total}</span></div>` +
        `<div class="progress"><span style="width:${pct}%"></span></div>`;
    },
    sym(ctx, id) {
      return ctx.symbols[id] ?? `#${id}`;
    },
    // 公版推斷的落下動態，外掛的 motion() 可以拿來改一部分再回傳
    motion: (ctx) => inferMotion(ctx),
  },
});

const state = {
  meta: null,     // /dev/meta：games、max_rounds、max_segments
  games: [],
  game: null,
  bets: [],       // 這批的每一局：{index, bet, win, isend, segments}
  betIdx: -1,
  steps: [],
  stepIdx: 0,
  timer: null,       // 自動播放的計時器；播到底就清掉，停下來等
  autoplay: true,    // 播放意圖：開著時 Spin、換局、選完 player 選項後自動接著播；用「自動」鈕或暫停（P）關掉，記在瀏覽器
  focusDetail: -1,
  pinned: new Set(), // 點一下釘住顯示格子編號的格子
  symMode: "name",   // 格子與中獎明細顯示圖標名稱（name）或盤面上的 ID（id）
  speed: 0,          // SPEEDS 的索引
  chain: false,      // 播放模式：false 單局（播完這局就停）、true 連續（接著播局列表的下一局）
  theme: "system",   // 主題：system（跟隨系統）、light、dark；記在瀏覽器
  status: null,      // 狀態列目前的訊息 { key, vars }，切換語言時照新語言重寫
  request: null,     // 產生目前這批的請求（choose、betmode、betmult、input、value、rounds），續段、回放與複製連結沿用；
                     // 從歷史局的回放連結載入時 input 為 "replay"，另帶 replay、h、n
  replay: null,      // 目前這局「回放這局」的比對結果 { ok, key, vars }
};

const $ = (id) => document.getElementById(id);

// 主題按鈕依序切換：跟隨系統 → 淺色 → 深色
const THEMES = {
  system: { label: "theme.system", next: "light", icon: `<rect x="3" y="4" width="18" height="12" rx="2" /><path d="M8 20h8M12 16v4" />` },
  light: { label: "theme.light", next: "dark", icon: `<circle cx="12" cy="12" r="4" /><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />` },
  dark: { label: "theme.dark", next: "system", icon: `<path d="M20 14.5A8 8 0 0 1 9.5 4a8 8 0 1 0 10.5 10.5z" />` },
};
const systemDark = matchMedia("(prefers-color-scheme: dark)");

// 播放速度固定三檔，不開放設定；畫面上以亮幾個箭頭表示第幾檔。
// drop 是格子落下的時間，stagger 是依序落下時每一順位的間隔，spread 是全部順位錯開的上限；turbo 不播落下
const SPEEDS = [
  { name: "normal", ms: 1350, drop: 320, stagger: 70, spread: 350 },
  { name: "fast", ms: 650, drop: 220, stagger: 40, spread: 180 },
  { name: "turbo", ms: 200, drop: 0, stagger: 0, spread: 0 },
];
const reducedMotion = matchMedia("(prefers-reduced-motion: reduce)");

function esc(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
}

// 錯誤回應是純文字（例如 "errlv=Warn invalid seed: …"）
async function api(path, body) {
  const res = await fetch(path, body ? { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) } : undefined);
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  return res.json();
}

// 瀏覽器儲存只記住上次選的機台、圖標的 Name／ID、主題、介面語言、自動播放、局列表與歷史局的贏倍篩選、起始資金；無法使用時照常運作。
// 玩家統計的紀錄另外存在 IndexedDB（stats.js）
function remember(key, value) {
  try {
    if (value === undefined) return localStorage.getItem(key);
    localStorage.setItem(key, value);
  } catch {
    return null;
  }
  return null;
}

// 狀態列：記下鍵與代入值，切換語言時重寫；key 為空時清空。warn 加上警告色
function setStatus(key, vars, warn = false) {
  state.status = key ? { key, vars, warn } : null;
  renderStatus();
}

function renderStatus() {
  const st = state.status;
  $("status").textContent = st ? (Array.isArray(st.key) ? st.key.map(([k, v]) => t(k, v)).join(t("status.sep")) : t(st.key, st.vars)) : "";
  $("status").classList.toggle("warn", !!(st && st.warn));
}

// 切換介面語言並記住；固定文字由 applyI18n 換掉，程式產生的部分整個重畫
function setLang(lang, save = true) {
  i18n.lang = LANGS.includes(lang) ? lang : "en";
  if (save) remember("dev.lang", i18n.lang);
  applyI18n();
  if (!state.game) return;
  setTheme(state.theme);
  setSpeed(state.speed);
  syncPlay();
  syncChain();
  syncInputs();
  syncWager();
  syncSeedCopy();
  renderStatus();
  renderSummary();
  renderResults();
  render();
  renderFundsChip();
  if ($("stats").open) renderStats();
}

// ---------- 載入 ----------

async function init() {
  i18n.lang = remember("dev.lang") === "zh" ? "zh" : "en";
  applyI18n();
  $("lang").onclick = (e) => {
    const btn = e.target.closest("button[data-lang]");
    if (btn) setLang(btn.dataset.lang);
  };
  state.meta = await api("meta");
  state.games = state.meta.games;
  // 應用端設定的預設語言：使用者在面板上選過語言（瀏覽器記住的）時以使用者為準，這裡不寫進瀏覽器
  if (remember("dev.lang") === null && state.meta.lang !== i18n.lang) setLang(state.meta.lang, false);
  // 應用端注入的主題排在 app.css 之後、外掛的 css 之前
  if (state.meta.theme) {
    const link = document.createElement("link");
    link.rel = "stylesheet";
    link.href = "games/theme.css";
    document.querySelector('link[href="app.css"]').after(link);
  }
  $("rounds").max = state.meta.max_rounds;
  $("betmult").max = state.meta.max_bet_mult;
  const sel = $("game");
  sel.innerHTML = state.games.map((g, i) => `<option value="${i}">${g.gid} ${esc(g.name)}</option>`).join("");
  // 網址參數可以直接重現某一局某一步：?gid=2&betmode=0&betmult=1&choose=first&input=seed&value=7&rounds=50&spin=1&bet=3&step=10&symmode=id
  // 歷史局的回放連結改帶 replay（整局的回放輸入）、h（結果雜湊）、n（局號）：?gid=2&betmode=0&betmult=1&replay=…&h=…&n=12&step=3
  const q = new URLSearchParams(location.search);
  const want = q.get("gid") ?? remember("dev.game");
  const idx = state.games.findIndex((g) => String(g.gid) === want);
  sel.value = String(Math.max(idx, 0));
  selectGame();
  if (q.has("betmode")) $("betmode").value = q.get("betmode");
  if (q.has("betmult")) $("betmult").value = q.get("betmult");
  if (q.has("choose")) $("choose").value = q.get("choose");
  if (q.has("input")) $("input").value = q.get("input");
  if (q.has("value")) $("value").value = q.get("value");
  if (q.has("rounds")) $("rounds").value = q.get("rounds");
  syncInputs();
  syncWager();
  state.symMode = (q.get("symmode") ?? remember("dev.symmode")) === "id" ? "id" : "name";
  $("symmode").textContent = symModeText();
  $("win-filter").checked = remember("dev.winfilter") === "1";
  const winMin = remember("dev.winmin");
  if (winMin !== null && winMin !== "") $("win-min").value = winMin;

  sel.onchange = selectGame;
  $("symmode").onclick = () => setSymMode(state.symMode === "id" ? "name" : "id");
  syncChain();
  $("chain").onclick = () => {
    state.chain = !state.chain;
    syncChain();
  };
  // 粗的實心箭頭，彼此緊貼
  const chevron = `<svg viewBox="0 0 12 16" width="12" height="16" aria-hidden="true"><path d="M0 0h5.5l6.5 8-6.5 8H0l6.5-8z" fill="currentColor" /></svg>`;
  setTheme(remember("dev.theme"));
  $("theme-btn").onclick = () => setTheme(THEMES[state.theme].next);
  systemDark.addEventListener("change", () => setTheme(state.theme));
  $("speed").innerHTML = SPEEDS.map((s, i) => `<button data-level="${i}" title="${s.name}">${chevron}</button>`).join("");
  $("speed").onclick = (e) => {
    const btn = e.target.closest("button");
    if (btn) setSpeed(Number(btn.dataset.level));
  };
  setSpeed(0);
  $("copy-link").onclick = (e) => copy(stepLink(), e.target);
  $("betmode").onchange = syncWager;
  $("betmult").oninput = syncWager;
  $("choose").onchange = syncInputs;
  $("input").onchange = syncInputs;
  $("spin").onclick = () => spin();
  $("seed-copy").onclick = (e) => copy($("seed-copy").dataset.seed, e.currentTarget);
  initStats();
  $("win-filter").onchange = () => {
    remember("dev.winfilter", $("win-filter").checked ? "1" : "0");
    renderResults();
  };
  // 改門檻就是要篩選，順手勾起來
  $("win-min").oninput = () => {
    remember("dev.winmin", $("win-min").value);
    if (!$("win-filter").checked) {
      $("win-filter").checked = true;
      remember("dev.winfilter", "1");
    }
    renderResults();
  };
  $("first").onclick = () => go(0);
  $("prev").onclick = () => go(state.stepIdx - 1);
  $("next").onclick = () => go(state.stepIdx + 1);
  $("last").onclick = () => go(state.steps.length - 1);
  $("play").onclick = togglePlay;
  state.autoplay = remember("dev.autoplay") !== "0";
  $("autoplay").onclick = () => {
    setAutoplay(!state.autoplay);
    syncPlay();
  };
  syncPlay();
  document.addEventListener("keydown", onKey);
  window.addEventListener("resize", () => renderBoard(context()));
  // 點格子：釘住／取消顯示該格編號
  $("board").addEventListener("click", (e) => {
    const cell = e.target.closest(".cell");
    if (!cell) return;
    const idx = Number(cell.dataset.idx);
    if (state.pinned.has(idx)) state.pinned.delete(idx); else state.pinned.add(idx);
    cell.classList.toggle("pin", state.pinned.has(idx));
  });

  // 從網址自動執行的 Spin 與回放連結都在重現已知的局，不計入玩家統計
  if (q.has("replay")) {
    await loadReplayLink(q);
    if (q.has("step")) go(Number(q.get("step")) - 1);
  } else if (q.get("spin") === "1") {
    await spin({ fromLink: true });
    if (q.has("bet")) selectBet(Number(q.get("bet")) - 1, false);
    if (q.has("step")) go(Number(q.get("step")) - 1);
  } else {
    render();
  }
}

function selectGame() {
  halt();
  state.game = state.games[Number($("game").value)];
  document.body.dataset.gid = state.game.gid; // 讓主題與外掛的 css 以 body[data-gid] 限定機台
  state.pinned.clear();
  remember("dev.game", String(state.game.gid));
  $("betmode").innerHTML = state.game.bet_units.map((u, i) => `<option value="${i}">${i}（${u}）</option>`).join("");
  syncWager();
  state.bets = [];
  state.betIdx = -1;
  state.steps = [];
  $("summary").textContent = "";
  loadPlugin(state.game);
  renderResults();
  render();
}

// Snap 與 player 一律只跑 1 局
function syncInputs() {
  const single = $("input").value === "snap" || $("choose").value === "player";
  $("rounds").disabled = single;
  if (single) $("rounds").value = 1;
  $("value").placeholder = t($("input").value === "snap" ? "bar.value.snap" : "bar.value.seed");
}

// 投注倍數 1～max_bet_mult；投注額 = 押注單位 × 倍數
function betMult() {
  const n = Math.trunc(Number($("betmult").value));
  return Math.max(1, Math.min(state.meta.max_bet_mult, n || 1));
}

function syncWager() {
  const unit = state.game.bet_units[Number($("betmode").value)] ?? 0;
  $("wager").textContent = t("bar.wager", { v: unit * betMult() });
}

// 只有 /dev/meta 標示 plugin 的機台才載入 games/<gid>.js；其他機台只顯示公版
function loadPlugin(game) {
  if (!game.plugin || DEV.plugins[game.gid] || document.querySelector(`script[data-gid="${game.gid}"]`)) return;
  const s = document.createElement("script");
  s.src = `games/${game.gid}.js`;
  s.dataset.gid = game.gid;
  s.onerror = () => setStatus("status.pluginFail", { gid: game.gid });
  document.body.appendChild(s);
}

// 手動按 Spin、輸入為 Seed 時，這批的局計入玩家統計（player 的局選到結束才計入）；fromLink 是從網址自動執行，不計入
async function spin(opts = {}) {
  halt();
  setStatus("status.running");
  const req = {
    gid: state.game.gid,
    betmode: Number($("betmode").value),
    betmult: betMult(),
    choose: $("choose").value,
    input: $("input").value,
    value: $("input").value === "snap" ? $("value").value.trim() : $("value").value,
    rounds: Number($("rounds").value) || 1,
  };
  try {
    const rep = await api("spin", req);
    state.request = {
      gid: req.gid, betmode: req.betmode, betmult: req.betmult, choose: req.choose,
      input: req.input, value: rep.input === "seed" ? rep.seed : req.value, rounds: rep.rounds,
    };
    $("betmult").value = req.betmult; // 超出範圍的輸入顯示成實際使用的倍數
    syncWager();
    state.bets = rep.bets || [];
    if (!opts.fromLink && req.input === "seed") {
      state.bets.forEach((b) => (b.record = true));
      statsAddEnded(state.bets);
    }
    // 實際使用的 seed 不寫在狀態列，改由「seed」按鈕複製
    $("seed-copy").dataset.seed = rep.input === "seed" ? rep.seed : "";
    syncSeedCopy();
    const notes = [];
    if (rep.input !== "seed") notes.push([rep.partial_replay ? "status.snapPartial" : "status.snap"]);
    if (rep.clamped) notes.push(["status.clamped", { n: rep.rounds }]);
    setStatus(notes.length ? notes : "");
    renderSummary();
    renderResults();
    // 從網址自動執行的不自動播放，停在網址指定的那一步
    const first = state.bets.findIndex(betVisible());
    selectBet(first >= 0 ? first : 0, !opts.fromLink);
  } catch (e) {
    setStatus("status.error", { e: e.message });
  }
}

function syncSeedCopy() {
  const seed = $("seed-copy").dataset.seed;
  $("seed-copy").hidden = !seed;
  $("seed-copy").title = seed ? t("bar.seed.tip", { seed }) : "";
}

// 總計只算已結束的局；player 的局選到結束後重新計算
function renderSummary() {
  let bet = 0, win = 0, done = 0;
  for (const b of state.bets) {
    if (!b.isend) continue;
    done++;
    bet += b.bet;
    win += b.win;
  }
  const rtp = bet > 0 ? ((100 * win) / bet).toFixed(2) : "0.00";
  const pending = state.bets.length - done;
  $("summary").textContent = state.bets.length
    ? t("list.summary", { done, bet, win, rtp }) + (pending ? t("list.pending", { n: pending }) : "")
    : "";
}

// ---------- 局與步 ----------

// 把一局所有段、所有 mode 的 act 攤平成一串步；沒有盤面的 act 沿用同一 mode 前一個盤面。
function flatten(b) {
  const steps = [];
  let lastExt = null;
  b.segments.forEach((seg, si) => {
    (seg.gamemodes || []).forEach((gm, mi) => {
      let screen = null;
      (gm.acts || []).forEach((act, ai) => {
        if (act.screen) screen = act.screen;
        if (act.ext !== undefined && act.ext !== null) lastExt = act.ext;
        steps.push({ segIdx: si, segment: seg, modeIdx: mi, mode: gm, act, screen, ext: lastExt, modeStart: ai === 0, segStart: mi === 0 && ai === 0 });
      });
    });
  });
  return steps;
}

// 換到第 i 局；播放意圖開著時從第 1 步接著播
function selectBet(i, play = true) {
  halt();
  showBet(i);
  if (play) resume();
}

// 換到第 i 局的第 1 步；連播換局時不停止播放
function showBet(i) {
  if (i < 0 || i >= state.bets.length) return;
  state.betIdx = i;
  state.steps = flatten(state.bets[i]);
  state.stepIdx = 0;
  state.replay = null;
  state.boardKey = null; // 重新 Spin 後同一個位置也要重播首盤高亮
  markActiveResult();
  render();
}

// 局列表的贏倍篩選：勾選「只看 ≥ X 倍」時只留贏分 > 0 且贏倍（贏分 ÷ 投注）≥ X 的局，X 為 0 時等於只看有贏分。
// 局列表、↑↓ 換局、連續播放與 Spin 後自動選的第一局都用這個判斷
function betVisible() {
  if (!$("win-filter").checked) return () => true;
  const min = Math.max(0, Number($("win-min").value) || 0);
  return (b) => b.win > 0 && b.win >= min * b.bet;
}

// 局列表上 dir 方向（-1 上一局、1 下一局）的下一個看得到的局
function nextBet(dir) {
  const visible = betVisible();
  for (let i = state.betIdx + dir; i >= 0 && i < state.bets.length; i += dir) {
    if (visible(state.bets[i])) return i;
  }
  return -1;
}

// 同一段、同一個 mode、同一個 round 的步屬於同一個 round
function roundKey(s) {
  return `${s.segIdx}:${s.modeIdx}:${s.act.round}`;
}

function roundStart(i) {
  const key = roundKey(state.steps[i]);
  while (i > 0 && roundKey(state.steps[i - 1]) === key) i--;
  return i;
}

// dir 1：下一個 round 的第一步；dir -1：上一個 round 的第一步，已經在第一個 round 時回到第 1 步
function goRound(dir) {
  if (!state.steps.length) return;
  if (dir < 0) {
    const start = roundStart(state.stepIdx);
    return go(start > 0 ? roundStart(start - 1) : 0);
  }
  const key = roundKey(state.steps[state.stepIdx]);
  const i = state.steps.findIndex((s, j) => j > state.stepIdx && roundKey(s) !== key);
  go(i < 0 ? state.steps.length - 1 : i);
}

// 手動換步（按鈕、方向鍵、時間軸）：停下這局的播放，但不關播放意圖，下一次 Spin 或換局照樣自動播
function go(i) {
  if (!state.steps.length) return;
  halt();
  showStep(i);
}

function showStep(i) {
  state.stepIdx = Math.max(0, Math.min(state.steps.length - 1, i));
  state.focusDetail = -1;
  render();
}

// 播放鍵（P）：播放中按下是暫停並關掉播放意圖；停著時按下是開始播並打開意圖，已在最後一步就從第 1 步重播
function togglePlay() {
  if (state.timer) {
    setAutoplay(false);
    return halt();
  }
  setAutoplay(true);
  if (!state.steps.length) return syncPlay();
  if (state.stepIdx >= state.steps.length - 1) showStep(0);
  resume();
}

function setAutoplay(on) {
  state.autoplay = on;
  remember("dev.autoplay", on ? "1" : "0");
}

// 播放意圖開著就從目前這一步往下播。播到這局最後一步時：連續模式且這局已結束就換到局列表的下一局；
// 單局模式、這局停在選擇點，或已經是最後一局時清掉計時器停下來等，播放意圖不變
function resume() {
  clearTimeout(state.timer);
  state.timer = null;
  if (state.autoplay && state.steps.length) {
    const tick = () => {
      if (state.stepIdx < state.steps.length - 1) {
        showStep(state.stepIdx + 1);
      } else {
        const next = state.chain && state.bets[state.betIdx].isend ? nextBet(1) : -1;
        if (next < 0) return halt();
        showBet(next);
      }
      state.timer = setTimeout(tick, SPEEDS[state.speed].ms);
    };
    if (state.stepIdx < state.steps.length - 1 || state.chain) state.timer = setTimeout(tick, SPEEDS[state.speed].ms);
  }
  syncPlay();
}

// 套用主題並記住選擇；跟隨系統時依目前的系統設定。index.html 開頭的 script 在畫面出現前先套用一次
function setTheme(theme) {
  state.theme = THEMES[theme] ? theme : "system";
  remember("dev.theme", state.theme);
  const dark = state.theme === "dark" || (state.theme === "system" && systemDark.matches);
  document.documentElement.dataset.theme = dark ? "dark" : "light";
  const th = THEMES[state.theme];
  $("theme-btn").title = t("theme.tip", { name: t(th.label) });
  $("theme-btn").setAttribute("aria-label", t("theme.aria", { name: t(th.label) }));
  $("theme-btn").innerHTML = `<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${th.icon}</svg>`;
}

// 播放中換檔從下一步開始生效
function setSpeed(level) {
  state.speed = ((level % SPEEDS.length) + SPEEDS.length) % SPEEDS.length;
  $("speed").title = t("ctrl.speed", { name: SPEEDS[state.speed].name });
  $("speed").querySelectorAll("button").forEach((btn, i) => {
    btn.classList.toggle("on", i <= state.speed);
    btn.setAttribute("aria-label", t("ctrl.speed.level", { name: SPEEDS[i].name }));
  });
}

// 只停計時器，不動播放意圖
function halt() {
  clearTimeout(state.timer);
  state.timer = null;
  syncPlay();
}

// 播放鍵顯示計時器的狀態；旁邊的「自動」鈕以亮綠表示播放意圖開著，還沒 Spin 時也能先切好
function syncPlay() {
  $("play").textContent = t(state.timer ? "ctrl.pause" : "ctrl.play");
  $("autoplay").setAttribute("aria-pressed", String(state.autoplay));
  $("autoplay").title = t(state.autoplay ? "ctrl.auto.on" : "ctrl.auto.off");
}

function syncChain() {
  $("chain").textContent = t(state.chain ? "ctrl.chain" : "ctrl.single");
}

function symModeText() {
  return state.symMode === "id" ? "SymbolID" : "SymbolName";
}

// 切換格子與中獎明細顯示名稱或 ID；記住上次的選擇
function setSymMode(mode) {
  state.symMode = mode === "id" ? "id" : "name";
  $("symmode").textContent = symModeText();
  remember("dev.symmode", state.symMode);
  const ctx = context();
  renderBoard(ctx);
  renderDetails(ctx);
}

// 快捷鍵一覽在 index.html 的說明面板（#help）。輸入框裡打字和選單操作不攔；Enter 在輸入框裡也執行 Spin。
function onKey(e) {
  const target = e.target;
  const typing = target instanceof Element && target.matches("input:not([type=checkbox]), select");
  // 統計面板開著時只處理 S（關閉）；Esc 由 dialog 自己關閉
  if ($("stats").open) {
    if (!typing && !e.metaKey && !e.ctrlKey && !e.altKey && (e.key === "s" || e.key === "S")) {
      e.preventDefault();
      toggleStats();
    }
    return;
  }
  if (e.key === "Enter" || (e.key === " " && !typing)) {
    e.preventDefault(); // 避免同時觸發焦點所在的按鈕
    spin();
    return;
  }
  if (typing || e.metaKey || e.ctrlKey || e.altKey) return;
  const help = $("help");
  const keys = {
    ArrowLeft: () => (e.shiftKey ? goRound(-1) : go(state.stepIdx - 1)),
    ArrowRight: () => (e.shiftKey ? goRound(1) : go(state.stepIdx + 1)),
    ArrowUp: () => { const i = nextBet(-1); if (i >= 0) selectBet(i); },
    ArrowDown: () => { const i = nextBet(1); if (i >= 0) selectBet(i); },
    Home: () => go(0),
    End: () => go(state.steps.length - 1),
    p: togglePlay,
    P: togglePlay,
    "+": () => setSpeed(state.speed + 1),
    "=": () => setSpeed(state.speed + 1), // 美式鍵盤上不按 Shift 的 + 鍵
    i: () => setSymMode(state.symMode === "id" ? "name" : "id"),
    I: () => setSymMode(state.symMode === "id" ? "name" : "id"),
    s: toggleStats,
    S: toggleStats,
    "?": () => help.togglePopover(),
    Escape: () => {
      if (help.matches(":popover-open")) return help.hidePopover();
      state.pinned.clear();
      renderBoard(context());
    },
  };
  const fn = keys[e.key];
  if (!fn) return;
  e.preventDefault(); // 方向鍵、Home／End 不捲動整頁
  fn();
}

// 目前這一步的網址：載入後以同一組參數執行，再跳到這一局的這一步
function stepLink() {
  const r = state.request;
  if (!r || state.betIdx < 0) return location.origin + location.pathname;
  if (r.input === "replay") return replayLink(r, state.stepIdx + 1);
  const q = new URLSearchParams({
    gid: r.gid, betmode: r.betmode, betmult: r.betmult, choose: r.choose, input: r.input, value: r.value,
    rounds: r.rounds, spin: 1, bet: state.betIdx + 1, step: state.stepIdx + 1,
  });
  if (state.symMode === "id") q.set("symmode", "id");
  return `${location.origin}${location.pathname}?${q}`;
}

// 只在重現不完整時顯示 ⚠，說明放在滑鼠提示，不撐寬控制列
function showLinkNote(note) {
  $("link-note").hidden = !note;
  $("link-note").title = note;
}

// 網址重現不了第 1 段以後的局：player 的選擇不在網址裡；Snap 只保證第 0 段
function linkNote() {
  const b = state.bets[state.betIdx];
  if (!b || b.segments.length < 2 || !state.request) return "";
  if (state.request.choose === "player") return t("link.player");
  if (state.request.input === "snap") return t("link.snap");
  return "";
}

// 只捲動容器本身，不帶動整頁，盤面才不會跳
function keepVisible(box, el, top = el && el.offsetTop) {
  if (!box || !el) return;
  const bottom = el.offsetTop + el.offsetHeight;
  if (top < box.scrollTop) box.scrollTop = top - 8;
  else if (bottom > box.scrollTop + box.clientHeight) box.scrollTop = bottom - box.clientHeight + 8;
}

// ---------- player 選擇 ----------

// 這局停在選擇點：以上一段的 choose_next、cp 與選擇的位置送出續段，新的段接在同一局後面。
async function pick(position) {
  const b = state.bets[state.betIdx];
  const last = b.segments[b.segments.length - 1];
  if (b.segments.length >= state.meta.max_segments) {
    setStatus("status.maxSeg", { n: state.meta.max_segments });
    return;
  }
  setStatus("status.running");
  try {
    const req = {
      gid: state.request.gid,
      betmode: state.request.betmode,
      betmult: state.request.betmult,
      cycle: b.segments.length,
      choice: position,
      choose_next: last.choose_next,
    };
    if (last.spin_state && last.spin_state.cp !== undefined) req.cp = last.spin_state.cp;
    const rep = await api("next", req);
    b.segments.push(rep.segment);
    b.win += rep.segment.win;
    b.isend = rep.segment.isend;
    if (b.isend) setStatus("status.betEnd", { n: b.index, win: b.win });
    else setStatus("status.pickNext");
    if (b.isend) statsAddEnded([b]);
    const from = state.steps.length;
    state.steps = flatten(b);
    state.stepIdx = Math.min(from, state.steps.length - 1);
    state.replay = null;
    renderSummary();
    renderResults();
    render();
    resume();
  } catch (e) {
    setStatus("status.error", { e: e.message });
  }
}

// ---------- 繪製 ----------

function context() {
  const step = state.steps[state.stepIdx];
  if (!step) return null;
  const modeInfo = state.game.modes[step.mode.modeid] || state.game.modes[0];
  const prev = state.steps[state.stepIdx - 1];
  const sameMode = prev && prev.segIdx === step.segIdx && prev.modeIdx === step.modeIdx;
  return {
    game: state.game,
    bet: state.bets[state.betIdx],
    segment: step.segment,
    segIdx: step.segIdx,
    modeIdx: step.modeIdx,
    mode: step.mode,
    act: step.act,
    ext: step.ext,
    prevExt: prev ? prev.ext : null,
    screen: step.screen,
    prevScreen: sameMode ? prev.screen : null,
    prevAct: sameMode ? prev.act : null,
    modeInfo,
    symbols: modeInfo.symbols || [],
  };
}

function plugin() {
  return DEV.plugins[state.game.gid] || {};
}

// 「State 1 · ModeID 2 [FreeGame]」中 State 後面的部分。State 是這一段 gamemodes 的索引，
// ModeID 是設定檔 game_mode_settings 的索引；名稱由外掛的 modes 提供，沒有就省略
function stateLabel(modeIdx, mode) {
  const name = pickLang((plugin().modes || {})[mode.modeid]);
  return `${modeIdx} · ModeID ${mode.modeid}${name ? ` [${name}]` : ""}`;
}

function render() {
  const ctx = context();
  renderInfo(ctx);
  renderBoard(ctx);
  renderTimeline();
  renderDetails(ctx);
  renderGamePanel(ctx);
  renderDTO(ctx);
}

// 分成兩塊：「Cycle N 快照」是目前這一步所在的 Cycle（段），換步到別的 Cycle 時跟著換；
// 「本局 DTO」是整局，「回放這局」以每個 Cycle 的快照逐一重跑
function renderDTO(ctx) {
  const b = state.bets[state.betIdx];
  if (!b || !ctx) {
    $("cycle-title").textContent = t("side.cycle");
    $("cycle-dto").innerHTML = "";
    $("dto").innerHTML = "";
    $("raw-dto").textContent = "";
    return;
  }
  const seg = ctx.segment;
  const st = seg.spin_state || {};
  $("cycle-title").textContent = t("side.cycleN", { i: ctx.segIdx }) + (b.segments.length > 1 ? t("side.cycleOf", { n: b.segments.length }) : "");
  $("cycle-dto").innerHTML =
    `<div class="muted">${esc(t("side.start"))}</div><code class="snap">${esc(st.start_b64u || "")}</code>` +
    `<div class="muted">${esc(t("side.after"))}</div><code class="snap">${esc(st.after_b64u || "")}</code>` +
    (st.cp !== undefined ? `<div class="muted">${esc(t("side.cp"))}</div><code class="snap">${esc(JSON.stringify(st.cp))}</code>` : "") +
    `<div class="btns">` +
    `<button id="dto-copy-snap">${esc(t("side.copyStart"))}</button>` +
    (ctx.segIdx === 0 ? `<button id="dto-snap" title="${esc(t("side.rerun.tip"))}">${esc(t("side.rerun"))}</button>` : "") +
    `</div>`;
  let html =
    `<div class="btns">` +
    `<button id="dto-replay" title="${esc(t("side.replay.tip"))}">${esc(t("side.replay"))}</button>` +
    `<button id="dto-copy">${esc(t("side.copyDto"))}</button></div>`;
  if (state.replay) html += `<div class="replay-result ${state.replay.ok ? "ok" : "bad"}">${esc(t(state.replay.key, state.replay.vars))}</div>`;
  $("dto").innerHTML = html;
  const json = JSON.stringify(b.segments, null, 2);
  $("raw-dto").textContent = json;
  $("dto-copy-snap").onclick = (e) => copy(st.start_b64u || "", e.target);
  $("dto-copy").onclick = (e) => copy(json, e.target);
  $("dto-replay").onclick = () => replayBet(b);
  if ($("dto-snap")) {
    $("dto-snap").onclick = () => {
      $("input").value = "snap";
      $("value").value = st.start_b64u || "";
      $("betmode").value = String(state.request.betmode);
      $("betmult").value = String(state.request.betmult);
      syncInputs();
      syncWager();
      spin();
    };
  }
}

// 以每一段的起始快照重跑整局（回放輸入見 stats.js 的 replaySegments），逐段比對
async function replayBet(b) {
  const segments = replaySegments(b);
  try {
    const rep = await api("replay", { gid: state.request.gid, betmode: state.request.betmode, betmult: state.request.betmult, segments });
    const got = rep.bet.segments;
    const bad = b.segments.findIndex((s, i) => JSON.stringify(s) !== JSON.stringify(got[i]));
    state.replay = bad < 0
      ? { ok: true, key: "side.replayOk", vars: { n: got.length } }
      : { ok: false, key: "side.replayBad", vars: { i: bad } };
  } catch (e) {
    state.replay = { ok: false, key: "side.replayFail", vars: { e: e.message } };
  }
  renderDTO(context());
}

async function copy(text, btn) {
  try {
    await navigator.clipboard.writeText(text);
    const old = btn.innerHTML; // 按鈕裡可能有圖示，整段換回
    btn.textContent = t("copy.done");
    setTimeout(() => (btn.innerHTML = old), 1000);
  } catch {
    window.prompt(t("copy.prompt"), text);
  }
}

// 局列表只在 Spin、篩選與 player 選擇後重建；切換局只改 active
function renderResults() {
  const visible = betVisible();
  $("results").innerHTML = state.bets
    .map((b, i) => {
      if (!visible(b)) return "";
      const fg = hasFG(b) ? `<span class="tag">FG</span>` : "";
      const seg = b.segments.length > 1 ? ` <span class="tag" title="${esc(t("list.cycles", { n: b.segments.length }))}">C×${b.segments.length}</span>` : "";
      const open = b.isend ? "" : ` <span class="tag">${esc(t("list.open"))}</span>`;
      const mult = b.bet ? (b.win / b.bet).toFixed(2) : "0";
      return `<li data-i="${i}"><span>#${b.index} ${fg}${seg}${open}</span><span>${b.win} (×${mult})</span></li>`;
    })
    .join("");
  $("results").querySelectorAll("li").forEach((li) => (li.onclick = () => selectBet(Number(li.dataset.i))));
  markActiveResult();
}

// 這局有沒有進 FG：某一段有一個以上的 game mode
function hasFG(b) {
  return b.segments.some((s) => (s.gamemodes || []).length > 1);
}

function markActiveResult() {
  let active = null;
  $("results").querySelectorAll("li").forEach((li) => {
    const on = Number(li.dataset.i) === state.betIdx;
    li.classList.toggle("active", on);
    if (on) active = li;
  });
  keepVisible($("results-panel"), active);
}

function renderInfo(ctx) {
  if (!ctx) {
    $("info").innerHTML = `<span class="muted">${esc(t("info.empty"))}</span>`;
    $("pos").textContent = "";
    showLinkNote("");
    return;
  }
  const { bet, segIdx, modeIdx, mode, act } = ctx;
  const items = [
    [t("info.bet"), state.request.betmult > 1 ? `${bet.bet} (×${state.request.betmult})` : bet.bet],
    [t("info.win"), bet.win],
    ["Cycle", bet.segments.length > 1 ? t("info.cycleOf", { i: segIdx, n: bet.segments.length }) : "0"],
    ["State", stateLabel(modeIdx, mode) + (mode.trigger ? t("info.trigger", { t: mode.trigger }) : "")],
    ["round", act.round],
    ["step", act.step],
    ["act", `${act.act} ${act.acttype}`],
    [t("info.actwin"), act.actwin],
    [t("info.stepacc"), act.stepaccwin],
    [t("info.roundacc"), act.roundaccwin],
  ];
  $("info").innerHTML = items.map(([k, v]) => `<span title="${esc(`${k} ${v}`)}"><span class="muted">${esc(k)}</span> <b>${esc(v)}</b></span>`).join("");
  $("pos").textContent = `${state.stepIdx + 1} / ${state.steps.length}`;
  showLinkNote(linkNote());
}

// H、L 依編號套色序 tone-1～tone-6，7 以後從 tone-1 重來並加上 faded；顏色都在 app.css 的變數，
// 可以由 theme.css 或外掛的 css 覆寫。C、S、W、Z 和其他圖標用各自的固定色。
const TONE_COUNT = 6;

// symLook 回傳格子的 class 陣列
function symLook(name) {
  const m = /^([A-Z])(\d+)$/.exec(name || "");
  const g = m ? m[1] : (name || "").charAt(0).toUpperCase();
  if (!m || (g !== "H" && g !== "L")) {
    return ["WSCZ".includes(g) && g ? `sym-${g}` : "sym-X"];
  }
  const n = Number(m[2]) - 1;
  const cls = [`sym-${g}`, `tone-${(((n % TONE_COUNT) + TONE_COUNT) % TONE_COUNT) + 1}`];
  if (n >= TONE_COUNT) cls.push("faded");
  return cls;
}

// ---------- 落下動態 ----------
// 每格的起點寫成 {dx, dy, order, fade}：dx、dy 是起點相對終點差幾格（dy 負數是在上方），
// order 是依序落下的順位（越大越晚），fade 表示新出現的圖標，一邊落下一邊淡入。

// 空格是名稱為 Z 系列的圖標（消除後還沒補的位置）
function isEmptySym(ctx, id) {
  return id >= 0 && symLook(ctx.symbols[id])[0] === "sym-Z";
}

// inferMotion 只比對前後兩張盤面，不看 acttype，推不出來的欄不給動態（照舊只有新圖標亮光）：
//   - 每個 round 的首盤：整盤從上方落下，由左到右一欄一欄依序落下。
//   - 某一欄上一步有被拿掉的格子（空格；上一步沒有空格時用上一步的中獎格），而這一步剩下的圖標
//     順序不變、全部落在這一欄最底下的位置：剩下的圖標從原本的列滑下來，上方新出現的圖標從盤面上緣補進來，
//     受影響的欄同時落下。負值格（不存在的格子）不動，圖標可以跨過它，與 ops.Gravity 相同。
function inferMotion(ctx) {
  const cur = ctx && ctx.act.screen;
  if (!cur) return null;
  const { columns, rows } = ctx.modeInfo;
  const out = new Map();
  const fresh = !ctx.prevAct || ctx.prevAct.round !== ctx.act.round;
  if (fresh) {
    cur.forEach((id, idx) => {
      if (id >= 0 && !isEmptySym(ctx, id)) out.set(idx, { dy: -rows, order: idx % columns, fade: true });
    });
    return out;
  }
  const prev = ctx.prevScreen;
  if (!prev || prev.length !== cur.length) return null;
  const hits = new Set();
  ((ctx.prevAct && ctx.prevAct.details) || []).forEach((d) => (d.hits || []).forEach((h) => hits.add(h)));
  const row = (idx) => Math.floor(idx / columns);
  for (let c = 0; c < columns; c++) {
    const slots = [];
    let same = true;
    for (let r = 0; r < rows; r++) {
      const idx = r * columns + c;
      if ((prev[idx] < 0) !== (cur[idx] < 0)) same = false;
      if (cur[idx] >= 0) slots.push(idx);
    }
    if (!same) continue;
    const holes = slots.some((i) => isEmptySym(ctx, prev[i]));
    const keep = slots.filter((i) => (holes ? !isEmptySym(ctx, prev[i]) : !hits.has(i)));
    if (keep.length === slots.length) continue;
    const tail = slots.slice(slots.length - keep.length);
    if (!tail.every((i, j) => cur[i] === prev[keep[j]])) continue;
    tail.forEach((i, j) => {
      const dy = row(keep[j]) - row(i);
      if (dy) out.set(i, { dy });
    });
    const added = slots.slice(0, slots.length - keep.length).filter((i) => !isEmptySym(ctx, cur[i]));
    if (added.length) {
      const span = row(added[added.length - 1]) + 1;
      added.forEach((i) => out.set(i, { dy: -span, fade: true }));
    }
  }
  return out;
}

// 這一步要播的動態：外掛的 motion() 回傳 false 不播，回傳物件或 Map（idx → 起點）就照它播，其他情況用公版推斷
function boardMotion(ctx, p) {
  if (!SPEEDS[state.speed].drop || reducedMotion.matches) return null;
  let m = p.motion ? p.motion(ctx) : undefined;
  if (m === false) return null;
  if (m === undefined || m === null || m === true) m = inferMotion(ctx);
  else if (!(m instanceof Map)) m = new Map(Object.entries(m).map(([k, v]) => [Number(k), v]));
  return m && m.size ? m : null;
}

// 依速度換算每格的延遲；順位多的時候縮小間隔，全部錯開不超過 spread
function motionTiming(motion) {
  const s = SPEEDS[state.speed];
  let maxOrder = 0;
  motion.forEach((m) => (maxOrder = Math.max(maxOrder, m.order || 0)));
  const stagger = maxOrder ? Math.min(s.stagger, s.spread / maxOrder) : 0;
  return { drop: s.drop, delay: (m) => Math.round((m.order || 0) * stagger) };
}

// 格子已經畫好之後再播，用 Web Animations 的 translate，不和 CSS 的 pop（transform）、亮光動畫互相蓋掉。
// 播的期間盤面裁掉外框以外的部分，從上方進來的格子不會蓋到資訊列
function playMotion(board, motion, timing, size) {
  const gs = getComputedStyle(board);
  const px = size + (parseFloat(gs.columnGap) || 0);
  const py = size + (parseFloat(gs.rowGap) || 0);
  const token = (state.motionToken = (state.motionToken || 0) + 1);
  board.classList.add("moving");
  const anims = [];
  motion.forEach((m, idx) => {
    const el = board.children[idx];
    if (!el) return;
    const from = `${(m.dx || 0) * px}px ${(m.dy || 0) * py}px`;
    const frames = m.fade
      ? [{ translate: from, opacity: 0 }, { opacity: 1, offset: 0.45 }, { translate: "0px 0px", opacity: 1 }]
      : [{ translate: from }, { translate: "0px 0px" }];
    anims.push(el.animate(frames, { duration: timing.drop, delay: timing.delay(m), easing: "cubic-bezier(.25, .7, .35, 1)", fill: "backwards" }).finished);
  });
  Promise.allSettled(anims).then(() => {
    if (state.motionToken === token) board.classList.remove("moving");
  });
}

function renderBoard(ctx) {
  const board = $("board");
  state.motionToken = (state.motionToken || 0) + 1;
  board.classList.remove("moving");
  if (!ctx || !ctx.screen) {
    board.innerHTML = "";
    return;
  }
  const { columns, rows } = ctx.modeInfo;
  const p = plugin();
  // 新圖標高亮只在換步時播；滑鼠移到中獎明細、調整視窗大小等重畫不重播
  const key = `${state.betIdx}:${state.stepIdx}`;
  const animate = state.boardKey !== key;
  state.boardKey = key;
  // 每個 round 的首盤（含 mode 的第一盤）整盤都是新圖標
  const fresh = !ctx.prevAct || ctx.prevAct.round !== ctx.act.round;
  const hits = new Set();
  const details = ctx.act.details || [];
  details.forEach((d, i) => {
    if (state.focusDetail < 0 || state.focusDetail === i) (d.hits || []).forEach((h) => hits.add(h));
  });
  const size = cellSize(columns, rows);
  board.style.setProperty("--cell", `${size}px`);
  board.style.gridTemplateColumns = `repeat(${columns}, ${size}px)`;
  board.classList.toggle("focus", state.focusDetail >= 0);
  const motion = animate ? boardMotion(ctx, p) : null;
  const timing = motion ? motionTiming(motion) : null;
  const cells = [];
  for (let idx = 0; idx < columns * rows; idx++) {
    const id = ctx.screen[idx];
    // 負值是不存在的格子：Trim 裁切掉的位置（-1）或遊戲封鎖的格子，畫成空位；名稱模式標一個淡淡的 ×
    if (id < 0) {
      cells.push(`<div class="cell void${state.pinned.has(idx) ? " pin" : ""}" data-idx="${idx}" title="idx ${idx} | ${t(id === -1 ? "board.trimmed" : "board.blocked")} (${id})"><span class="idx">${idx}</span>` +
        `${state.symMode === "id" ? id : `<span class="void-x">×</span>`}</div>`);
      continue;
    }
    const name = ctx.symbols[id] ?? (id === undefined ? "" : `#${id}`);
    const cls = ["cell", ...symLook(name)];
    if (state.pinned.has(idx)) cls.push("pin");
    if (hits.has(idx)) cls.push(animate ? "hit pop" : "hit");
    // 往下滑的舊圖標不算新圖標，不加亮光；落下的格子等落定後才開始亮光與 pop
    const mv = motion && motion.get(idx);
    if (animate && ctx.act.screen && (fresh || (ctx.prevScreen && ctx.prevScreen[idx] !== id)) && !(mv && !mv.fade)) cls.push("changed");
    if (p.cellClass) cls.push(...[].concat(p.cellClass(idx, ctx) || []));
    const badge = p.badge ? pickLang(p.badge(idx, ctx)) : "";
    // 外掛的 label 取代中央的圖標名稱；ID 模式是用來和後端陣列逐格比對的，不呼叫 label。
    // 回傳 ""、null、undefined、false 都用公版預設（false 讓 `cond && "x"` 的寫法也能用）
    const raw = state.symMode === "id" || !p.label ? "" : pickLang(p.label(idx, ctx));
    const label = raw === null || raw === undefined || raw === false ? "" : String(raw);
    if (label) cls.push("labeled", ...(label.length >= 5 ? ["long"] : []));
    const style = mv ? ` style="--after:${timing.delay(mv) + timing.drop}ms"` : "";
    cells.push(
      // 名稱模式下 Z 系列是空格（消除後還沒補的位置），不寫名稱；ID 模式每一格都顯示盤面上的原始數字。
      // data-sym 與 title 一律是原圖標，配色和覆寫規則不受 label 影響
      `<div class="${cls.join(" ")}" data-idx="${idx}" data-sym="${esc(name)}"${style} title="idx ${idx} | ${esc(name)} (id ${id})"><span class="idx">${idx}</span>` +
      `${state.symMode === "id" ? id : label ? esc(label) : cls[1] === "sym-Z" ? "" : esc(name)}` +
      `${badge ? `<span class="badge">${esc(badge)}</span>` : ""}</div>`
    );
  }
  board.innerHTML = cells.join("");
  if (motion) playMotion(board, motion, timing, size);
}

// 格子大小只跟視窗和盤面行列數有關，換步時不變；至少留 TIMELINE_MIN 給時間軸
const TIMELINE_MIN = 200;
let sizeCache = { key: "", size: 64 };

function cellSize(columns, rows) {
  const stage = $("board").parentElement;
  const key = `${columns}x${rows}@${stage.clientWidth}x${stage.clientHeight}`;
  if (sizeCache.key === key) return sizeCache.size;
  const fixed = $("info").offsetHeight + $("board").nextElementSibling.offsetHeight + 30 + 18; // 列距與盤面內距
  const byH = (stage.clientHeight - fixed - TIMELINE_MIN - (rows - 1) * 4) / rows;
  const byW = (stage.clientWidth - 18 - (columns - 1) * 4) / columns;
  const size = Math.max(32, Math.min(64, Math.floor(Math.min(byH, byW))));
  sizeCache = { key, size };
  return size;
}

// 依 段 > Mode > Round 分組列出每個 act；只有一段的局不顯示段的層級
function renderTimeline() {
  const p = plugin();
  const ctxBase = context();
  const b = state.bets[state.betIdx];
  const segs = []; // [{segIdx, segment, modes: [{modeIdx, mode, rounds: [{round, items: [[step, i]]}]}]}]
  state.steps.forEach((s, i) => {
    let sg = segs[segs.length - 1];
    if (!sg || s.segStart) segs.push((sg = { segIdx: s.segIdx, segment: s.segment, modes: [] }));
    let g = sg.modes[sg.modes.length - 1];
    if (!g || s.modeStart) sg.modes.push((g = { modeIdx: s.modeIdx, mode: s.mode, rounds: [] }));
    let r = g.rounds[g.rounds.length - 1];
    if (!r || r.round !== s.act.round) g.rounds.push((r = { round: s.act.round, items: [] }));
    r.items.push([s, i]);
  });
  const showSeg = b && (b.segments.length > 1 || !b.isend);
  const modeHTML = (g) => {
    const head = `State ${esc(stateLabel(g.modeIdx, g.mode))}` +
      ` <span class="muted">${esc(t("tl.win", { v: g.mode.win }) + (g.mode.trigger ? t("tl.trigger", { t: g.mode.trigger }) : ""))}</span>`;
    const rounds = g.rounds
      .map((r) => {
        const last = r.items[r.items.length - 1][0].act;
        const acts = r.items
          .map(([s, i]) => {
            const cls = ["tl-act"];
            if (i === state.stepIdx) cls.push("active");
            if (s.act.actwin > 0) cls.push("win");
            const label = p.actLabel && ctxBase ? pickLang(p.actLabel(s.act, ctxBase)) : s.act.acttype;
            return `<span data-i="${i}" class="${cls.join(" ")}" title="step ${s.act.step} | act ${s.act.act} | ${esc(s.act.acttype)}">` +
              `${esc(label)}${s.act.actwin > 0 ? ` +${s.act.actwin}` : ""}</span>`;
          })
          .join("");
        return `<div class="tl-round"><span class="tl-round-head" title="${esc(t("tl.roundacc", { v: last.roundaccwin }))}">Round ${r.round}</span>` +
          `<span class="tl-acts">${acts}</span></div>`;
      })
      .join("");
    return `<div class="tl-mode"><div class="tl-mode-head">${head}</div>${rounds}</div>`;
  };
  let html = segs
    .map((sg) => {
      const modes = sg.modes.map(modeHTML).join("");
      if (!showSeg) return modes;
      const picked = sg.segIdx > 0 && sg.segment.choice !== undefined ? t("tl.picked", { i: sg.segment.choice }) : "";
      const head = `Cycle ${sg.segIdx} <span class="muted">${esc(t("tl.win", { v: sg.segment.win }) + picked + (sg.segment.isend ? "" : t("tl.atChoice")))}</span>`;
      return `<div class="tl-seg"><div class="tl-seg-head">${head}</div>${modes}</div>`;
    })
    .join("");
  // player：這局停在選擇點時列出選項
  if (b && !b.isend) {
    const last = b.segments[b.segments.length - 1];
    html += `<div class="choices"><span class="muted">${esc(t("tl.choose"))}</span>` +
      (last.choose_next || []).map((entry, i) => `<button data-choice="${i}" title="${esc(t("tl.choice.tip", { i, e: entry }))}">#${i} → ${esc(entry)}</button>`).join("") +
      `</div>`;
  }
  $("timeline").innerHTML = html;
  $("timeline").querySelectorAll(".tl-act").forEach((el) => (el.onclick = () => go(Number(el.dataset.i))));
  $("timeline").querySelectorAll("[data-choice]").forEach((el) => (el.onclick = () => pick(Number(el.dataset.choice))));
  // 捲到目前這個 round；是該 Mode 第一個 round 時連 Mode 標題一起露出來
  const active = $("timeline").querySelector(".tl-act.active");
  if (active) {
    const round = active.closest(".tl-round"), mode = round.parentElement;
    const top = round === mode.querySelector(".tl-round") ? mode.offsetTop : round.offsetTop;
    keepVisible($("timeline"), active, top);
  }
}

function renderDetails(ctx) {
  const tbl = $("details");
  const details = (ctx && ctx.act.details) || [];
  if (!details.length) {
    tbl.innerHTML = `<tbody><tr><td class="muted">${esc(t("side.none"))}</td></tr></tbody>`;
    return;
  }
  tbl.innerHTML =
    `<thead><tr>${["side.symbol", "side.count", "side.where", "side.winCol"].map((k) => `<th>${esc(t(k))}</th>`).join("")}</tr></thead><tbody>` +
    details
      .map((d, i) => {
        const where = d.comb ? t("side.comb", { n: d.comb }) : `L${d.line}${d.direction ? " ←" : ""}`;
        const name = DEV.util.sym(ctx, d.symbol);
        const sym = state.symMode === "id" ? d.symbol : name;
        return `<tr data-i="${i}"><td title="${esc(name)} (id ${d.symbol})">${esc(sym)}</td><td>${d.count}</td>` +
          `<td>${where}</td><td>${d.win}</td></tr>`;
      })
      .join("") +
    `</tbody>`;
  tbl.querySelectorAll("tbody tr").forEach((tr) => {
    tr.onmouseenter = () => { state.focusDetail = Number(tr.dataset.i); renderBoard(context()); };
    tr.onmouseleave = () => { state.focusDetail = -1; renderBoard(context()); };
  });
}

function renderGamePanel(ctx) {
  const p = plugin();
  $("game-panel-title").textContent = pickLang(p.title) || t("side.game");
  $("raw-ext").textContent = ctx && ctx.ext ? JSON.stringify(ctx.ext, null, 2) : t("side.noExt");
  if (!ctx) {
    $("game-panel").innerHTML = "";
    return;
  }
  if (!p.panel) {
    $("game-panel").innerHTML = `<span class="muted">${esc(t("side.noPlugin"))}</span>`;
    return;
  }
  try {
    $("game-panel").innerHTML = pickLang(p.panel(ctx)) || "";
  } catch (e) {
    $("game-panel").innerHTML = `<span class="muted">${esc(t("side.pluginErr", { gid: state.game.gid, e: e.message }))}</span>`;
  }
}

init().catch((e) => setStatus("status.loadFail", { e: e.message }));
