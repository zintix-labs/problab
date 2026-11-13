// ProbLab Dev 公版：機台與押注選擇、局列表、盤面、逐步播放、中獎明細、player 選擇與回放。
// 機台專屬的畫法放在 games/<gid>.js，透過 DEV.register(gid, plugin) 掛上來，介面見 README.md。
"use strict";

const DEV = (window.DEV = {
  plugins: {},
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
  timer: null,
  focusDetail: -1,
  pinned: new Set(), // 點一下釘住顯示格子編號的格子
  symMode: "name",   // 格子與中獎明細顯示圖標名稱（name）或盤面上的 ID（id）
  request: null,     // 產生目前這批的請求（choose、betmode、betmult），續段與回放沿用
  replay: null,      // 目前這局「回放這局」的比對結果
};

const $ = (id) => document.getElementById(id);

function esc(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
}

// 錯誤回應是純文字（例如 "errlv=Warn invalid seed: …"）
async function api(path, body) {
  const res = await fetch(path, body ? { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) } : undefined);
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  return res.json();
}

// 瀏覽器儲存只記住上次選的機台；無法使用時照常運作
function remember(key, value) {
  try {
    if (value === undefined) return localStorage.getItem(key);
    localStorage.setItem(key, value);
  } catch {
    return null;
  }
  return null;
}

// ---------- 載入 ----------

async function init() {
  state.meta = await api("meta");
  state.games = state.meta.games;
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
  $("symmode").value = state.symMode;

  sel.onchange = selectGame;
  $("symmode").onchange = () => setSymMode($("symmode").value);
  $("betmode").onchange = syncWager;
  $("betmult").oninput = syncWager;
  $("choose").onchange = syncInputs;
  $("input").onchange = syncInputs;
  $("spin").onclick = spin;
  $("only-win").onchange = renderResults;
  $("first").onclick = () => go(0);
  $("prev").onclick = () => go(state.stepIdx - 1);
  $("next").onclick = () => go(state.stepIdx + 1);
  $("last").onclick = () => go(state.steps.length - 1);
  $("play").onclick = togglePlay;
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

  if (q.get("spin") === "1") {
    await spin();
    if (q.has("bet")) selectBet(Number(q.get("bet")) - 1);
    if (q.has("step")) go(Number(q.get("step")) - 1);
  } else {
    render();
  }
}

function selectGame() {
  stop();
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
  $("value").placeholder = $("input").value === "snap" ? "某一段的 start_b64u（只保證第 0 段）" : "空白隨機｜整數｜hex:…｜utf8:…";
}

// 投注倍數 1～max_bet_mult；投注額 = 押注單位 × 倍數
function betMult() {
  const n = Math.trunc(Number($("betmult").value));
  return Math.max(1, Math.min(state.meta.max_bet_mult, n || 1));
}

function syncWager() {
  const unit = state.game.bet_units[Number($("betmode").value)] ?? 0;
  $("wager").textContent = `投注額 ${unit * betMult()}`;
}

// 只有 /dev/meta 標示 plugin 的機台才載入 games/<gid>.js；其他機台只顯示公版
function loadPlugin(game) {
  if (!game.plugin || DEV.plugins[game.gid] || document.querySelector(`script[data-gid="${game.gid}"]`)) return;
  const s = document.createElement("script");
  s.src = `games/${game.gid}.js`;
  s.dataset.gid = game.gid;
  s.onerror = () => ($("status").textContent = `games/${game.gid}.js 載入失敗，只顯示公版`);
  document.body.appendChild(s);
}

async function spin() {
  stop();
  $("status").textContent = "執行中…";
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
    state.request = { gid: req.gid, betmode: req.betmode, betmult: req.betmult, choose: req.choose };
    $("betmult").value = req.betmult; // 超出範圍的輸入顯示成實際使用的倍數
    syncWager();
    state.bets = rep.bets || [];
    const notes = [];
    if (rep.input === "seed") notes.push(`seed ${rep.seed}`);
    else notes.push(rep.partial_replay ? "snap 回放（只保證第 0 段）" : "snap 回放");
    if (rep.clamped) notes.push(`已限制為 ${rep.rounds} 局`);
    $("status").textContent = notes.join("｜");
    renderSummary();
    renderResults();
    const firstWin = state.bets.findIndex((b) => b.win > 0);
    selectBet($("only-win").checked && firstWin >= 0 ? firstWin : 0);
  } catch (e) {
    $("status").textContent = "錯誤：" + e.message;
  }
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
    ? `${done} 局｜總押 ${bet}｜總贏 ${win}｜RTP ${rtp}%` + (pending ? `｜${pending} 局進行中` : "")
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

function selectBet(i) {
  stop();
  if (i < 0 || i >= state.bets.length) return;
  state.betIdx = i;
  state.steps = flatten(state.bets[i]);
  state.stepIdx = 0;
  state.replay = null;
  state.boardKey = null; // 重新 Spin 後同一個位置也要重播首盤高亮
  markActiveResult();
  render();
}

function go(i) {
  if (!state.steps.length) return;
  state.stepIdx = Math.max(0, Math.min(state.steps.length - 1, i));
  state.focusDetail = -1;
  render();
}

function togglePlay() {
  if (state.timer) return stop();
  if (state.stepIdx >= state.steps.length - 1) go(0);
  $("play").textContent = "⏸ 暫停";
  const tick = () => {
    if (state.stepIdx >= state.steps.length - 1) return stop();
    go(state.stepIdx + 1);
    state.timer = setTimeout(tick, Number($("speed").value));
  };
  state.timer = setTimeout(tick, Number($("speed").value));
}

function stop() {
  clearTimeout(state.timer);
  state.timer = null;
  $("play").textContent = "▶ 播放";
}

// 切換格子與中獎明細顯示名稱或 ID；記住上次的選擇
function setSymMode(mode) {
  state.symMode = mode === "id" ? "id" : "name";
  $("symmode").value = state.symMode;
  remember("dev.symmode", state.symMode);
  const ctx = context();
  renderBoard(ctx);
  renderDetails(ctx);
}

// Space／Enter：Spin；←／→：上一步／下一步；I：切換名稱／ID。輸入框裡打字和選單操作不攔。
function onKey(e) {
  const t = e.target;
  const typing = t.matches("input:not([type=checkbox]), select");
  if (e.key === "Enter" || (e.key === " " && !typing)) {
    e.preventDefault(); // 避免同時觸發焦點所在的按鈕
    spin();
  } else if (!typing && e.key === "ArrowLeft") {
    e.preventDefault();
    go(state.stepIdx - 1);
  } else if (!typing && e.key === "ArrowRight") {
    e.preventDefault();
    go(state.stepIdx + 1);
  } else if (!typing && (e.key === "i" || e.key === "I") && !e.metaKey && !e.ctrlKey && !e.altKey) {
    setSymMode(state.symMode === "id" ? "name" : "id");
  }
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
    $("status").textContent = `錯誤：一局最多 ${state.meta.max_segments} 段`;
    return;
  }
  $("status").textContent = "執行中…";
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
    $("status").textContent = b.isend ? `第 ${b.index} 局結束，總贏分 ${b.win}` : "請選擇下一段";
    const from = state.steps.length;
    state.steps = flatten(b);
    state.stepIdx = Math.min(from, state.steps.length - 1);
    state.replay = null;
    renderSummary();
    renderResults();
    render();
  } catch (e) {
    $("status").textContent = "錯誤：" + e.message;
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

function render() {
  const ctx = context();
  renderInfo(ctx);
  renderBoard(ctx);
  renderTimeline();
  renderDetails(ctx);
  renderGamePanel(ctx);
  renderDTO(ctx);
}

// 本局 DTO：每一段的 start_b64u 是該段實際使用的起始快照；「回放這局」以每一段的快照逐段重跑
function renderDTO(ctx) {
  const b = state.bets[state.betIdx];
  if (!b || !ctx) {
    $("dto").innerHTML = "";
    $("raw-dto").textContent = "";
    return;
  }
  const seg = ctx.segment;
  const st = seg.spin_state || {};
  const multi = b.segments.length > 1;
  let html = multi ? `<div class="seg-title">第 ${ctx.segIdx} 段（共 ${b.segments.length} 段）</div>` : "";
  html +=
    `<div class="muted">start_b64u（本段起始快照）</div><code class="snap">${esc(st.start_b64u || "")}</code>` +
    `<div class="muted">after_b64u（本段結束後）</div><code class="snap">${esc(st.after_b64u || "")}</code>` +
    (st.cp !== undefined ? `<div class="muted">cp（本段輸出的 checkpoint）</div><code class="snap">${esc(JSON.stringify(st.cp))}</code>` : "") +
    `<div class="btns">` +
    (ctx.segIdx === 0 ? `<button id="dto-snap" title="輸入切到 Snap，只保證第 0 段">用這段快照重跑</button>` : "") +
    `<button id="dto-copy-snap">複製 start_b64u</button>` +
    `<button id="dto-replay" title="以每一段的快照逐段重跑，整局完全重現">回放這局</button>` +
    `<button id="dto-copy">複製完整 DTO</button></div>`;
  if (state.replay) html += `<div class="replay-result ${state.replay.ok ? "ok" : "bad"}">${esc(state.replay.text)}</div>`;
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

// 回放輸入：每一段的起始快照；續段的 choose_next 與 cp 取自上一段
async function replayBet(b) {
  const segments = b.segments.map((s, i) => {
    const seg = { start_b64u: s.spin_state.start_b64u, cycle: i };
    if (i > 0) {
      const prev = b.segments[i - 1];
      seg.choice = s.choice;
      seg.choose_next = prev.choose_next;
      if (prev.spin_state.cp !== undefined) seg.cp = prev.spin_state.cp;
    }
    return seg;
  });
  try {
    const rep = await api("replay", { gid: state.request.gid, betmode: state.request.betmode, betmult: state.request.betmult, segments });
    const got = rep.bet.segments;
    const bad = b.segments.findIndex((s, i) => JSON.stringify(s) !== JSON.stringify(got[i]));
    state.replay = bad < 0
      ? { ok: true, text: `回放一致：${got.length} 段完全相同` }
      : { ok: false, text: `回放不一致：第 ${bad} 段不同` };
  } catch (e) {
    state.replay = { ok: false, text: "回放失敗：" + e.message };
  }
  renderDTO(context());
}

async function copy(text, btn) {
  try {
    await navigator.clipboard.writeText(text);
    const old = btn.textContent;
    btn.textContent = "已複製";
    setTimeout(() => (btn.textContent = old), 1000);
  } catch {
    window.prompt("複製下面的內容", text);
  }
}

// 局列表只在 Spin、篩選與 player 選擇後重建；切換局只改 active
function renderResults() {
  const onlyWin = $("only-win").checked;
  $("results").innerHTML = state.bets
    .map((b, i) => {
      if (onlyWin && !(b.win > 0)) return "";
      const fg = b.segments.some((s) => (s.gamemodes || []).length > 1) ? `<span class="tag">FG</span>` : "";
      const seg = b.segments.length > 1 ? ` <span class="tag">${b.segments.length} 段</span>` : "";
      const open = b.isend ? "" : ` <span class="tag">進行中</span>`;
      const mult = b.bet ? (b.win / b.bet).toFixed(2) : "0";
      return `<li data-i="${i}"><span>#${b.index} ${fg}${seg}${open}</span><span>${b.win}（×${mult}）</span></li>`;
    })
    .join("");
  $("results").querySelectorAll("li").forEach((li) => (li.onclick = () => selectBet(Number(li.dataset.i))));
  markActiveResult();
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
    $("info").innerHTML = `<span class="muted">按 Spin 產生結果，再從左邊選一局。</span>`;
    $("pos").textContent = "";
    return;
  }
  const { bet, segIdx, mode, act } = ctx;
  const items = [
    ["押注", state.request.betmult > 1 ? `${bet.bet}（×${state.request.betmult}）` : bet.bet],
    ["總贏分", bet.win],
    ["段", bet.segments.length > 1 ? `${segIdx}（共 ${bet.segments.length} 段）` : "0"],
    ["Mode", `${mode.modeid}${mode.trigger ? `（觸發 ${mode.trigger}）` : ""}`],
    ["round", act.round],
    ["step", act.step],
    ["act", `${act.act} ${act.acttype}`],
    ["本 act", act.actwin],
    ["step 累積", act.stepaccwin],
    ["round 累積", act.roundaccwin],
  ];
  $("info").innerHTML = items.map(([k, v]) => `<span><span class="muted">${k}</span> <b>${esc(v)}</b></span>`).join("");
  $("pos").textContent = `${state.stepIdx + 1} / ${state.steps.length}`;
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

function renderBoard(ctx) {
  const board = $("board");
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
  const cells = [];
  for (let idx = 0; idx < columns * rows; idx++) {
    const id = ctx.screen[idx];
    // 負值是不存在的格子：Trim 裁切掉的位置（-1）或遊戲封鎖的格子，畫成空位；名稱模式標一個淡淡的 ×
    if (id < 0) {
      cells.push(`<div class="cell void${state.pinned.has(idx) ? " pin" : ""}" data-idx="${idx}" title="idx ${idx}｜${id === -1 ? "裁切" : "封鎖"}（${id}）"><span class="idx">${idx}</span>` +
        `${state.symMode === "id" ? id : `<span class="void-x">×</span>`}</div>`);
      continue;
    }
    const name = ctx.symbols[id] ?? (id === undefined ? "" : `#${id}`);
    const cls = ["cell", ...symLook(name)];
    if (state.pinned.has(idx)) cls.push("pin");
    if (hits.has(idx)) cls.push(animate ? "hit pop" : "hit");
    if (animate && ctx.act.screen && (fresh || (ctx.prevScreen && ctx.prevScreen[idx] !== id))) cls.push("changed");
    if (p.cellClass) cls.push(...[].concat(p.cellClass(idx, ctx) || []));
    const badge = p.badge ? p.badge(idx, ctx) : "";
    cells.push(
      // 名稱模式下 Z 系列是空格（消除後還沒補的位置），不寫名稱；ID 模式每一格都顯示盤面上的原始數字
      `<div class="${cls.join(" ")}" data-idx="${idx}" data-sym="${esc(name)}" title="idx ${idx}｜${esc(name)}（id ${id}）"><span class="idx">${idx}</span>` +
      `${state.symMode === "id" ? id : cls[1] === "sym-Z" ? "" : esc(name)}` +
      `${badge ? `<span class="badge">${esc(badge)}</span>` : ""}</div>`
    );
  }
  board.innerHTML = cells.join("");
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
  const segs = []; // [{segIdx, segment, modes: [{mode, rounds: [{round, items: [[step, i]]}]}]}]
  state.steps.forEach((s, i) => {
    let sg = segs[segs.length - 1];
    if (!sg || s.segStart) segs.push((sg = { segIdx: s.segIdx, segment: s.segment, modes: [] }));
    let g = sg.modes[sg.modes.length - 1];
    if (!g || s.modeStart) sg.modes.push((g = { mode: s.mode, rounds: [] }));
    let r = g.rounds[g.rounds.length - 1];
    if (!r || r.round !== s.act.round) g.rounds.push((r = { round: s.act.round, items: [] }));
    r.items.push([s, i]);
  });
  const showSeg = b && (b.segments.length > 1 || !b.isend);
  const modeHTML = (g) => {
    const head = `Mode ${g.mode.modeid}` +
      ` <span class="muted">贏分 ${g.mode.win}${g.mode.trigger ? `｜觸發 ${g.mode.trigger}` : ""}</span>`;
    const rounds = g.rounds
      .map((r) => {
        const last = r.items[r.items.length - 1][0].act;
        const acts = r.items
          .map(([s, i]) => {
            const cls = ["tl-act"];
            if (i === state.stepIdx) cls.push("active");
            if (s.act.actwin > 0) cls.push("win");
            const label = p.actLabel && ctxBase ? p.actLabel(s.act, ctxBase) : s.act.acttype;
            return `<span data-i="${i}" class="${cls.join(" ")}" title="step ${s.act.step}｜act ${s.act.act}｜${esc(s.act.acttype)}">` +
              `${esc(label)}${s.act.actwin > 0 ? ` +${s.act.actwin}` : ""}</span>`;
          })
          .join("");
        return `<div class="tl-round"><span class="tl-round-head" title="round 累積 ${last.roundaccwin}">Round ${r.round}</span>` +
          `<span class="tl-acts">${acts}</span></div>`;
      })
      .join("");
    return `<div class="tl-mode"><div class="tl-mode-head">${head}</div>${rounds}</div>`;
  };
  let html = segs
    .map((sg) => {
      const modes = sg.modes.map(modeHTML).join("");
      if (!showSeg) return modes;
      const picked = sg.segIdx > 0 && sg.segment.choice !== undefined ? `｜選 #${sg.segment.choice}` : "";
      const head = `段 ${sg.segIdx} <span class="muted">贏分 ${sg.segment.win}${picked}${sg.segment.isend ? "" : "｜結束於選擇點"}</span>`;
      return `<div class="tl-seg"><div class="tl-seg-head">${head}</div>${modes}</div>`;
    })
    .join("");
  // player：這局停在選擇點時列出選項
  if (b && !b.isend) {
    const last = b.segments[b.segments.length - 1];
    html += `<div class="choices"><span class="muted">選擇下一段：</span>` +
      (last.choose_next || []).map((entry, i) => `<button data-choice="${i}" title="位置 ${i}，入口 ${entry}">#${i} → ${esc(entry)}</button>`).join("") +
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
    tbl.innerHTML = `<tbody><tr><td class="muted">無</td></tr></tbody>`;
    return;
  }
  tbl.innerHTML =
    `<thead><tr><th>圖標</th><th>數量</th><th>線／組合</th><th>贏分</th></tr></thead><tbody>` +
    details
      .map((d, i) => {
        const where = d.comb ? `${d.comb} 組` : `L${d.line}${d.direction ? " ←" : ""}`;
        const name = DEV.util.sym(ctx, d.symbol);
        const sym = state.symMode === "id" ? d.symbol : name;
        return `<tr data-i="${i}"><td title="${esc(name)}（id ${d.symbol}）">${esc(sym)}</td><td>${d.count}</td>` +
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
  $("game-panel-title").textContent = p.title || "機台資訊";
  $("raw-ext").textContent = ctx && ctx.ext ? JSON.stringify(ctx.ext, null, 2) : "（這一步沒有 ext）";
  if (!ctx) {
    $("game-panel").innerHTML = "";
    return;
  }
  if (!p.panel) {
    $("game-panel").innerHTML = `<span class="muted">這台沒有注入外掛，只顯示公版。</span>`;
    return;
  }
  try {
    $("game-panel").innerHTML = p.panel(ctx) || "";
  } catch (e) {
    $("game-panel").innerHTML = `<span class="muted">games/${state.game.gid}.js 錯誤：${esc(e.message)}</span>`;
  }
}

init().catch((e) => ($("status").textContent = "載入失敗：" + e.message));
