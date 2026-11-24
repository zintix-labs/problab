// ProbLab Dev 玩家統計：把手動 Spin（Seed 模式）的每一局累計成一個玩家 session，跨機台、跨押注模式，
// 直到按「歸零」為止。資料存在瀏覽器的 IndexedDB，同一個瀏覽器的所有 dev 分頁共用一份。
//
// 每局存一筆紀錄：回放輸入（每個 Cycle 的快照、choice、choose_next、cp）、結果的雜湊，以及到這一局為止的累計值。
// 累計值跟著每一筆走，所以只保留最近 STATS_KEEP 局時，總計、最高／最低資金與走勢的數值仍然是整個 session 的。
"use strict";

const STATS_KEEP = 10000;      // 保留可重播的最近幾局
const STATS_PAGE = 500;        // 歷史局一次列出幾筆
const STATS_FUNDS = 10000;     // 起始資金預設值
const STATS_DB = "problab-dev";
const STATS_STORE = "history";

const stats = {
  db: null,       // Promise<IDBDatabase | null>；瀏覽器不能用 IndexedDB 時為 null，統計停用但頁面照常
  records: [],    // 保留的紀錄，由舊到新
  view: "funds",  // 走勢圖：funds（資金）或 rtp
  shown: STATS_PAGE,
  resetArmed: null,
};

// ---------- 紀錄 ----------

// 一局的回放輸入：每一段的起始快照；續段的 choice、choose_next 與 cp 取自上一段。/dev/replay 照這份輸入能完全重現整局
function replaySegments(b) {
  return b.segments.map((s, i) => {
    const seg = { start_b64u: s.spin_state.start_b64u, cycle: i };
    if (i > 0) {
      const prev = b.segments[i - 1];
      seg.choice = s.choice;
      seg.choose_next = prev.choose_next;
      if (prev.spin_state.cp !== undefined) seg.cp = prev.spin_state.cp;
    }
    return seg;
  });
}

// 整局結果的短雜湊（cyrb53），重播時比對，不一致代表遊戲設定或程式改過
function betHash(b) {
  const s = JSON.stringify(b.segments);
  let h1 = 0xdeadbeef, h2 = 0x41c6ce57;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    h1 = Math.imul(h1 ^ c, 2654435761);
    h2 = Math.imul(h2 ^ c, 1597334677);
  }
  h1 = Math.imul(h1 ^ (h1 >>> 16), 2246822507) ^ Math.imul(h2 ^ (h2 >>> 13), 3266489909);
  h2 = Math.imul(h2 ^ (h2 >>> 16), 2246822507) ^ Math.imul(h1 ^ (h1 >>> 13), 3266489909);
  return (4294967296 * (2097151 & h2) + (h1 >>> 0)).toString(16);
}

// 接在上一筆後面的新紀錄；net 是累計淨輸贏（贏分 − 押注），hi／lo 是到這一局為止 net 的最高／最低（含開局的 0），
// cm 是贏倍的累計（倍率 RTP = cm ÷ n），mx 是最大贏倍
function statsNext(prev, item) {
  const p = prev || { n: 0, cb: 0, cw: 0, cm: 0, cf: 0, net: 0, hi: 0, lo: 0, mx: 0 };
  const mult = item.bet > 0 ? item.win / item.bet : 0;
  const net = p.net + item.win - item.bet;
  return {
    ...item,
    n: p.n + 1, t: Date.now(),
    cb: p.cb + item.bet, cw: p.cw + item.win, cm: p.cm + mult, cf: p.cf + (item.fg ? 1 : 0),
    net, hi: Math.max(p.hi, net), lo: Math.min(p.lo, net), mx: Math.max(p.mx, mult),
  };
}

function statsDB() {
  if (!stats.db) {
    stats.db = new Promise((resolve) => {
      try {
        const req = indexedDB.open(STATS_DB, 1);
        req.onupgradeneeded = () => req.result.createObjectStore(STATS_STORE, { keyPath: "n" });
        req.onsuccess = () => resolve(req.result);
        req.onerror = () => resolve(null);
      } catch {
        resolve(null);
      }
    });
  }
  return stats.db;
}

function statsTx(db, mode, fn) {
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STATS_STORE, mode);
    let out;
    tx.oncomplete = () => resolve(out);
    tx.onerror = tx.onabort = () => reject(tx.error || new Error("IndexedDB 交易失敗"));
    fn(tx.objectStore(STATS_STORE), (v) => (out = v));
  });
}

// 在同一個交易裡先讀最後一筆再接著寫，多個分頁同時寫入也不會撞號；超過 STATS_KEEP 的舊紀錄一併刪掉
async function statsAppend(items) {
  const db = await statsDB();
  if (!db || !items.length) return;
  await statsTx(db, "readwrite", (st) => {
    st.openCursor(null, "prev").onsuccess = (e) => {
      const cur = e.target.result;
      let last = cur ? cur.value : null;
      for (const item of items) {
        last = statsNext(last, item);
        st.put(last);
      }
      if (last.n > STATS_KEEP) st.delete(IDBKeyRange.upperBound(last.n - STATS_KEEP));
    };
  });
}

async function statsLoad() {
  const db = await statsDB();
  stats.records = db ? await statsTx(db, "readonly", (st, done) => (st.getAll().onsuccess = (e) => done(e.target.result))) : [];
}

// 這批裡已結束、還沒計入的局寫進統計。只有手動 Spin 的 Seed 局帶 record；player 的局選到結束才計入
function statsAddEnded(bets) {
  const r = state.request;
  const todo = bets.filter((b) => b.record && b.isend && !b.recorded);
  if (!r || !todo.length) return;
  todo.forEach((b) => (b.recorded = true));
  const items = todo.map((b) => ({
    gid: r.gid, betmode: r.betmode, betmult: r.betmult,
    bet: b.bet, win: b.win, fg: hasFG(b), segs: replaySegments(b), h: betHash(b),
  }));
  statsAppend(items)
    .then(() => {
      renderFundsChip();
      if ($("stats").open) refreshStats();
    })
    .catch((e) => ($("status").textContent = "統計寫入失敗：" + e.message));
}

// ---------- 回放連結 ----------

function toB64u(text) {
  const bytes = new TextEncoder().encode(text);
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function fromB64u(s) {
  const bin = atob(s.replace(/-/g, "+").replace(/_/g, "/"));
  return new TextDecoder().decode(Uint8Array.from(bin, (c) => c.charCodeAt(0)));
}

// 歷史局的網址：帶著整局的回放輸入，打開後呼叫 /dev/replay 只跑這一局；h 用來比對，n 是 session 裡的局號
function replayLink(r, step) {
  const q = new URLSearchParams({ gid: r.gid, betmode: r.betmode, betmult: r.betmult, replay: r.replay, h: r.h, n: r.n });
  if (step) q.set("step", step);
  if (state.symMode === "id") q.set("symmode", "id");
  return `${location.origin}${location.pathname}?${q}`;
}

// 從回放連結載入：只跑這一局，不計入統計；結果與紀錄的雜湊不同時在狀態列警告
async function loadReplayLink(q) {
  const n = Number(q.get("n")) || 1;
  if (String(state.game.gid) !== q.get("gid")) {
    $("status").textContent = `錯誤：找不到機台 ${q.get("gid")}`;
    render();
    return;
  }
  $("status").textContent = "重播中…";
  try {
    const segments = JSON.parse(fromB64u(q.get("replay")));
    const req = { gid: state.game.gid, betmode: Number($("betmode").value), betmult: betMult(), segments };
    const rep = await api("replay", req);
    const b = rep.bet;
    b.index = n;
    state.request = {
      gid: req.gid, betmode: req.betmode, betmult: req.betmult, choose: "", input: "replay", value: "", rounds: 1,
      replay: q.get("replay"), h: q.get("h") || "", n,
    };
    state.bets = [b];
    const same = !q.get("h") || betHash(b) === q.get("h");
    $("status").textContent = same
      ? `歷史局 #${n} 重播（不計入統計）`
      : `⚠ 歷史局 #${n} 的重播結果與紀錄不同：遊戲設定或程式可能改過`;
    $("status").classList.toggle("warn", !same);
    renderSummary();
    renderResults();
    selectBet(0);
  } catch (e) {
    $("status").textContent = "重播失敗：" + e.message;
    render();
  }
}

// ---------- 頂部欄的剩餘資金 ----------

// 只讀最後一筆就夠：累計淨輸贏跟著每一筆紀錄走
async function statsLast() {
  const db = await statsDB();
  if (!db) return null;
  return statsTx(db, "readonly", (st, done) => (st.openCursor(null, "prev").onsuccess = (e) => done(e.target.result ? e.target.result.value : null)));
}

async function renderFundsChip() {
  const last = await statsLast().catch(() => null);
  const v = statsFunds() + (last ? last.net : 0);
  $("funds-val").textContent = fmtNum(v);
  $("funds-chip").classList.toggle("neg", v < 0);
}

// ---------- 面板 ----------

function initStats() {
  const funds = Number(remember("dev.funds"));
  $("stats-funds").value = remember("dev.funds") !== null && Number.isFinite(funds) ? funds : STATS_FUNDS;
  $("stats-win-filter").checked = remember("dev.stats.winfilter") === "1";
  const winMin = remember("dev.stats.winmin");
  if (winMin !== null && winMin !== "") $("stats-win-min").value = winMin;

  $("stats-btn").onclick = toggleStats;
  $("funds-chip").onclick = openStats;
  renderFundsChip();
  // 別的分頁也可能寫入統計，回到這個分頁時更新剩餘資金
  document.addEventListener("visibilitychange", () => { if (!document.hidden) renderFundsChip(); });
  $("stats-close").onclick = () => $("stats").close();
  $("stats-funds").oninput = () => {
    remember("dev.funds", $("stats-funds").value);
    renderStats();
    renderFundsChip();
  };
  $("stats-reset").onclick = resetStats;
  $("stats-view").onclick = (e) => {
    const btn = e.target.closest("button[data-view]");
    if (!btn) return;
    stats.view = btn.dataset.view;
    renderStats();
  };
  $("stats-win-filter").onchange = () => {
    remember("dev.stats.winfilter", $("stats-win-filter").checked ? "1" : "0");
    stats.shown = STATS_PAGE;
    renderStatsList();
  };
  $("stats-win-min").oninput = () => {
    remember("dev.stats.winmin", $("stats-win-min").value);
    if (!$("stats-win-filter").checked) {
      $("stats-win-filter").checked = true;
      remember("dev.stats.winfilter", "1");
    }
    stats.shown = STATS_PAGE;
    renderStatsList();
  };
  $("stats-list").onclick = (e) => {
    const btn = e.target.closest("button");
    if (!btn) return;
    if (btn.dataset.more !== undefined) {
      stats.shown += STATS_PAGE;
      renderStatsList();
      return;
    }
    const r = stats.records.find((x) => x.n === Number(btn.closest("li").dataset.n));
    if (!r) return;
    const link = replayLink({ ...r, replay: toB64u(JSON.stringify(r.segs)) });
    if (btn.dataset.act === "open") window.open(link, "_blank", "noopener");
    else copy(link, btn);
  };
  const chart = $("stats-chart");
  chart.onmousemove = (e) => statsHover(e);
  chart.onmouseleave = () => statsHover(null);
  window.addEventListener("resize", () => { if ($("stats").open) renderStatsChart(); });
}

function toggleStats() {
  if ($("stats").open) $("stats").close();
  else openStats();
}

async function openStats() {
  $("stats").showModal();
  $("stats").focus(); // 焦點放在面板本身而不是起始資金的輸入框，S 才能直接關閉
  stats.shown = STATS_PAGE;
  await refreshStats();
}

async function refreshStats() {
  await statsLoad();
  renderStats();
}

// 歸零要按兩次：第一次變成確認，3 秒內再按一次才清空
async function resetStats() {
  const btn = $("stats-reset");
  if (!stats.resetArmed) {
    btn.textContent = "再按一次確認歸零";
    btn.classList.add("danger");
    stats.resetArmed = setTimeout(() => {
      stats.resetArmed = null;
      btn.textContent = "歸零";
      btn.classList.remove("danger");
    }, 3000);
    return;
  }
  clearTimeout(stats.resetArmed);
  stats.resetArmed = null;
  btn.textContent = "歸零";
  btn.classList.remove("danger");
  const db = await statsDB();
  if (db) await statsTx(db, "readwrite", (st) => st.clear());
  stats.shown = STATS_PAGE;
  await refreshStats();
  renderFundsChip();
}

function statsFunds() {
  const v = Number($("stats-funds").value);
  return Number.isFinite(v) ? v : STATS_FUNDS;
}

const fmtNum = (v, digits = 0) => v.toLocaleString("en-US", { minimumFractionDigits: digits, maximumFractionDigits: digits });
const fmtPct = (v) => `${fmtNum(v, 2)}%`;

// 第一次資金跌破 0 的局：lo 是到該局為止的最低淨輸贏，所以找第一筆 funds + lo < 0 的紀錄。
// 保留範圍的第一筆就已經破產時，可能更早就破產了
function statsBust(funds) {
  const recs = stats.records;
  const i = recs.findIndex((r) => funds + r.lo < 0);
  if (i < 0) return null;
  return { index: i, n: recs[i].n, early: i === 0 && recs[0].n > 1 };
}

function renderStats() {
  const recs = stats.records;
  const last = recs[recs.length - 1];
  const funds = statsFunds();
  $("stats-view").querySelectorAll("button").forEach((b) => b.classList.toggle("on", b.dataset.view === stats.view));
  $("stats-list-title").textContent = `歷史局（最近 ${fmtNum(STATS_KEEP)} 局）`;
  if (!last) {
    $("stats-nums").innerHTML = `<span class="muted">${stats.db ? "還沒有紀錄。在 Seed 模式按 Spin 後開始累計。" : "這個瀏覽器無法使用 IndexedDB，統計停用。"}</span>`;
    renderStatsChart();
    renderStatsList();
    return;
  }
  const bust = statsBust(funds);
  const items = [
    ["轉數", fmtNum(last.n)],
    ["總押注", fmtNum(last.cb)],
    ["總贏分", fmtNum(last.cw)],
    ["資金 RTP", last.cb > 0 ? fmtPct((100 * last.cw) / last.cb) : "—", "總贏分 ÷ 總押注"],
    ["倍率 RTP", fmtPct((100 * last.cm) / last.n), "每局贏倍（贏分 ÷ 該局押注）的平均"],
    ["進 FG", last.cf ? `${fmtNum(last.cf)} 次（1/${fmtNum(last.n / last.cf, 1)}）` : "0 次", "進過 FG 的局數；括號是平均幾局一次"],
    ["目前資金", fmtNum(funds + last.net)],
    ["最高資金", fmtNum(funds + last.hi)],
    ["最低資金", fmtNum(funds + last.lo)],
    ["最大贏倍", `×${fmtNum(last.mx, 2)}`],
  ];
  if (bust) items.push(["破產", bust.early ? `第 ${fmtNum(bust.n)} 局或更早` : `第 ${fmtNum(bust.n)} 局`, "資金第一次跌破 0"]);
  $("stats-nums").innerHTML = items
    .map(([k, v, tip]) => `<div${tip ? ` title="${esc(tip)}"` : ""}${k === "破產" ? ` class="bust"` : ""}><span class="muted">${k}</span><b>${esc(v)}</b></div>`)
    .join("");
  renderStatsChart();
  renderStatsList();
}

// 走勢圖一次畫一條線，三種視圖都用主題綠，靠切換按鈕與圖例標明目前是哪一個
function statsSeries(funds) {
  if (stats.view === "rtp") {
    return { lines: [{ cls: "s-a", name: "資金 RTP", get: (r) => (r.cb > 0 ? (100 * r.cw) / r.cb : 0) }], hint: "總贏分 ÷ 總押注", refs: [100], fmt: fmtPct, rtp: true };
  }
  if (stats.view === "mrtp") {
    return { lines: [{ cls: "s-a", name: "倍率 RTP", get: (r) => (100 * r.cm) / r.n }], hint: "每局贏倍的平均", refs: [100], fmt: fmtPct, rtp: true };
  }
  return { lines: [{ cls: "s-a", name: "資金", get: (r) => funds + r.net }], hint: "起始資金 + 累計淨輸贏", refs: [funds, 0], fmt: (v) => fmtNum(v) };
}

const CHART = { h: 220, l: 72, r: 14, t: 12, b: 24 };

function renderStatsChart() {
  const box = $("stats-chart");
  const recs = stats.records;
  const legend = $("stats-legend");
  if (recs.length < 2) {
    box.innerHTML = `<div class="stats-empty muted">至少兩局才畫走勢</div>`;
    legend.innerHTML = "";
    stats.chart = null;
    return;
  }
  const funds = statsFunds();
  const s = statsSeries(funds);
  const W = Math.max(320, box.clientWidth);
  const pw = W - CHART.l - CHART.r;
  const ph = CHART.h - CHART.t - CHART.b;
  const vals = s.lines.map((ln) => recs.map(ln.get));
  // RTP 在開頭幾局會劇烈跳動（第一局 ×20 就是 2000%），縱軸範圍跳過開頭 5%（最多 200 局）再決定，超出的部分在圖框邊緣截掉
  const from = s.rtp && recs[0].n === 1 ? Math.min(Math.floor(recs.length * 0.05), 200) : 0;
  let lo = Infinity, hi = -Infinity;
  vals.forEach((v) => { for (let i = from; i < v.length; i++) { if (v[i] < lo) lo = v[i]; if (v[i] > hi) hi = v[i]; } });
  // 參考線只在範圍附近時納入，避免為了畫 0 把曲線壓扁
  s.refs.forEach((ref) => { const span = Math.max(hi - lo, 1e-9); if (ref >= lo - span * 0.25 && ref <= hi + span * 0.25) { lo = Math.min(lo, ref); hi = Math.max(hi, ref); } });
  if (hi - lo < 1e-9) { hi += 1; lo -= 1; }
  const padY = (hi - lo) * 0.06;
  lo -= padY;
  hi += padY;
  const x = (i) => CHART.l + (pw * i) / (recs.length - 1);
  const y = (v) => CHART.t + ph * (1 - (v - lo) / (hi - lo));
  // 點太多時每個像素欄只留最高與最低，峰谷不會被抽樣抽掉
  const path = (v) => {
    const cols = Math.max(1, Math.floor(pw));
    if (v.length <= cols * 2) return v.map((val, i) => `${i ? "L" : "M"}${x(i).toFixed(1)},${y(val).toFixed(1)}`).join("");
    let d = "";
    for (let c = 0; c < cols; c++) {
      const a = Math.floor((c * v.length) / cols), z = Math.floor(((c + 1) * v.length) / cols);
      let mi = a, ma = a;
      for (let i = a; i < z; i++) { if (v[i] < v[mi]) mi = i; if (v[i] > v[ma]) ma = i; }
      for (const i of mi < ma ? [mi, ma] : [ma, mi]) d += `${d ? "L" : "M"}${x(i).toFixed(1)},${y(v[i]).toFixed(1)}`;
    }
    return d;
  };
  const ticks = [hi - padY, (hi + lo) / 2, lo + padY];
  // 山形圖：線下方填一層由上往下淡出的漸層
  const fades = s.lines.map((ln, k) =>
    `<linearGradient id="stats-fade-${k}" class="fade" x1="0" y1="0" x2="0" y2="1">` +
    `<stop offset="0" stop-opacity="0.32" /><stop offset="1" stop-opacity="0" /></linearGradient>`).join("");
  let svg = `<svg width="${W}" height="${CHART.h}" viewBox="0 0 ${W} ${CHART.h}" aria-label="走勢圖">` +
    `<defs>${fades}<clipPath id="stats-clip"><rect x="${CHART.l}" y="${CHART.t}" width="${pw}" height="${ph}" /></clipPath></defs>`;
  ticks.forEach((t) => {
    svg += `<line class="grid" x1="${CHART.l}" x2="${W - CHART.r}" y1="${y(t).toFixed(1)}" y2="${y(t).toFixed(1)}" />` +
      `<text class="axis" x="${CHART.l - 8}" y="${(y(t) + 4).toFixed(1)}" text-anchor="end">${esc(s.fmt(t))}</text>`;
  });
  s.refs.forEach((ref) => {
    if (ref < lo || ref > hi) return;
    svg += `<line class="ref" x1="${CHART.l}" x2="${W - CHART.r}" y1="${y(ref).toFixed(1)}" y2="${y(ref).toFixed(1)}" />`;
  });
  const base = CHART.t + ph;
  const lines = vals.map((v) => path(v));
  lines.forEach((d, k) => (svg += `<path class="area" d="${d}L${CHART.l + pw},${base}L${CHART.l},${base}Z" fill="url(#stats-fade-${k})" clip-path="url(#stats-clip)" />`));
  lines.forEach((d, k) => (svg += `<path class="line ${s.lines[k].cls}" d="${d}" clip-path="url(#stats-clip)" />`));
  if (stats.view === "funds") {
    const bust = statsBust(funds);
    if (bust && !bust.early) svg += `<circle class="bust-dot" cx="${x(bust.index).toFixed(1)}" cy="${y(vals[0][bust.index]).toFixed(1)}" r="4.5"><title>第 ${bust.n} 局破產</title></circle>`;
  }
  svg += `<text class="axis" x="${CHART.l}" y="${CHART.h - 6}">#${fmtNum(recs[0].n)}</text>` +
    `<text class="axis" x="${W - CHART.r}" y="${CHART.h - 6}" text-anchor="end">#${fmtNum(recs[recs.length - 1].n)}</text>` +
    `<line id="stats-guide" class="guide" x1="0" x2="0" y1="${CHART.t}" y2="${CHART.t + ph}" visibility="hidden" />` +
    s.lines.map((ln, k) => `<circle class="guide-dot ${ln.cls}" data-k="${k}" r="3.5" visibility="hidden" />`).join("") +
    `</svg>`;
  box.innerHTML = svg;
  legend.innerHTML = s.lines.map((ln) => `<span class="legend ${ln.cls}"><i></i>${esc(ln.name)}</span>`).join("") + `<span class="muted">${esc(s.hint)}</span>`;
  stats.chart = { x, y, vals, s, pw };
  statsHover(null);
}

// 滑鼠移到走勢圖上：顯示最近一局的局號與數值
function statsHover(e) {
  const c = stats.chart;
  const out = $("stats-hover");
  const guide = $("stats-guide");
  if (!c || !guide) return;
  const dots = $("stats-chart").querySelectorAll(".guide-dot");
  if (!e) {
    guide.setAttribute("visibility", "hidden");
    dots.forEach((d) => d.setAttribute("visibility", "hidden"));
    out.textContent = "";
    return;
  }
  const recs = stats.records;
  const px = e.clientX - $("stats-chart").getBoundingClientRect().left;
  const i = Math.max(0, Math.min(recs.length - 1, Math.round(((px - CHART.l) / c.pw) * (recs.length - 1))));
  const gx = c.x(i).toFixed(1);
  guide.setAttribute("x1", gx);
  guide.setAttribute("x2", gx);
  guide.setAttribute("visibility", "visible");
  dots.forEach((d) => {
    const cy = c.y(c.vals[Number(d.dataset.k)][i]);
    d.setAttribute("cx", gx);
    d.setAttribute("cy", cy.toFixed(1));
    d.setAttribute("visibility", cy >= CHART.t && cy <= CHART.h - CHART.b ? "visible" : "hidden");
  });
  out.textContent = `#${fmtNum(recs[i].n)}　` + c.s.lines.map((ln, k) => `${ln.name} ${c.s.fmt(c.vals[k][i])}`).join("　");
}

function statsVisible() {
  if (!$("stats-win-filter").checked) return () => true;
  const min = Math.max(0, Number($("stats-win-min").value) || 0);
  return (r) => r.win > 0 && r.win >= min * r.bet;
}

function gameName(gid) {
  const g = state.games.find((x) => x.gid === gid);
  return g ? `${gid} ${g.name}` : String(gid);
}

// 歷史局由新到舊，一次列 STATS_PAGE 筆，往下按「顯示更多」
function renderStatsList() {
  const visible = statsVisible();
  const rows = [];
  for (let i = stats.records.length - 1; i >= 0; i--) if (visible(stats.records[i])) rows.push(stats.records[i]);
  const html = rows.slice(0, stats.shown).map((r) => {
    const mult = r.bet > 0 ? r.win / r.bet : 0;
    return `<li data-n="${r.n}" title="贏分 ${fmtNum(r.win)}｜倍數 ${r.betmult}｜${r.segs.length} 個 Cycle">` +
      `<span class="n">#${fmtNum(r.n)}</span><span class="g">${esc(gameName(r.gid))}</span><span>BetMode ${r.betmode}</span>` +
      `<span>${r.fg ? `<span class="tag">FG</span>` : ""}</span><span class="num">押 ${fmtNum(r.bet)}</span><span class="num">×${fmtNum(mult, 2)}</span>` +
      `<span class="acts"><button data-act="open">開新視窗</button><button data-act="copy">複製連結</button></span></li>`;
  });
  if (rows.length > stats.shown) html.push(`<li class="more"><button data-more>顯示更多（還有 ${fmtNum(rows.length - stats.shown)} 局）</button></li>`);
  if (!rows.length) html.push(`<li class="more muted">${stats.records.length ? "沒有符合篩選的局" : "沒有紀錄"}</li>`);
  $("stats-list").innerHTML = html.join("");
}
