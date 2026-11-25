// ProbLab Dev 玩家統計：把手動 Spin（Seed 模式）的每一局累計成一個玩家 session，跨機台、跨押注模式，
// 直到按「歸零」為止。資料存在瀏覽器的 IndexedDB，同一個瀏覽器的所有 dev 分頁共用一份。
//
// 兩個儲存區：
//   - history：每局一筆紀錄，帶回放輸入（每個 Cycle 的快照、choice、choose_next、cp）、結果的雜湊與到這一局為止的累計值，
//     只保留最近 STATS_KEEP 局，用於歷史局列表與重播。
//   - meta 的 series：整個 session 的走勢資料，最多 SERIES_MAX 格；滿了就兩兩合併，每格涵蓋的局數加倍。
//     每格記錄結尾的累計值、格內各數值的最高／最低點、格內最大回撤與最大贏倍，合併後峰谷與回撤都不會被抹平。
// 走勢圖、滑鼠讀值與區間選取用同一串「點」：保留範圍內是逐局紀錄，更早的部分是走勢資料的格子。
"use strict";

const STATS_KEEP = 10000;      // 保留可重播的最近幾局
const STATS_PAGE = 500;        // 歷史局一次列出幾筆
const STATS_FUNDS = 10000;     // 起始資金預設值
const SERIES_MAX = 2048;       // 走勢資料最多幾格；圖寬不到一千像素，這個數量已經比螢幕能顯示的細
const STATS_DB = "problab-dev";
const STATS_STORE = "history";
const STATS_META = "meta";

const stats = {
  db: null,        // Promise<IDBDatabase | null>；瀏覽器不能用 IndexedDB 時為 null，統計停用但頁面照常
  records: [],     // 保留的紀錄，由舊到新
  series: null,    // 走勢資料 { size, buckets }
  units: [],       // 走勢圖的點（見 statsUnits）
  view: "funds",   // 走勢圖：funds（資金）、rtp（資金 RTP）、mrtp（倍率 RTP）
  sel: null,       // 區間選取 { i, j, dragging }：units 的索引
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

// ---------- 走勢資料 ----------
// 一格涵蓋第 a～b 局。net、cb、cw、cm、cf、lo、hi 是第 b 局的累計值；
// v 依序是資金（net）、資金 RTP、倍率 RTP 在格內的 [最低, 最低的局號, 最高, 最高的局號]；
// dd 是格內最大回撤（淨輸贏從格內高點往後掉最多的幅度），pm 是格內最大贏倍。

const rtpOf = (u) => (u.cb > 0 ? (100 * u.cw) / u.cb : NaN);
const mrtpOf = (u) => (u.b > 0 ? (100 * u.cm) / u.b : NaN);

function bucketOf(r) {
  const rtp = rtpOf({ cb: r.cb, cw: r.cw }), mrtp = (100 * r.cm) / r.n;
  return {
    a: r.n, b: r.n, net: r.net, cb: r.cb, cw: r.cw, cm: r.cm, cf: r.cf, lo: r.lo, hi: r.hi,
    v: [[r.net, r.n, r.net, r.n], [rtp, r.n, rtp, r.n], [mrtp, r.n, mrtp, r.n]],
    dd: 0, pm: r.bet > 0 ? r.win / r.bet : 0,
  };
}

// x 在前、y 在後的兩格合併；最高最低相同時取較早的
function bucketMerge(x, y) {
  const v = x.v.map((p, k) => {
    const q = y.v[k];
    return [...(p[0] <= q[0] ? [p[0], p[1]] : [q[0], q[1]]), ...(p[2] >= q[2] ? [p[2], p[3]] : [q[2], q[3]])];
  });
  return { ...y, a: x.a, v, dd: Math.max(x.dd, y.dd, x.v[0][2] - y.v[0][0]), pm: Math.max(x.pm, y.pm) };
}

function seriesAdd(doc, r) {
  const bs = doc.buckets;
  const last = bs[bs.length - 1];
  if (last && last.b - last.a + 1 < doc.size) bs[bs.length - 1] = bucketMerge(last, bucketOf(r));
  else bs.push(bucketOf(r));
  if (bs.length > SERIES_MAX) {
    const out = [];
    for (let i = 0; i < bs.length; i += 2) out.push(i + 1 < bs.length ? bucketMerge(bs[i], bs[i + 1]) : bs[i]);
    doc.buckets = out;
    doc.size *= 2;
  }
}

const emptySeries = () => ({ k: "series", size: 1, buckets: [] });

// ---------- IndexedDB ----------

// 第 2 版加上 meta（走勢資料）；從第 1 版升級時，用現存的紀錄補出走勢資料（更早的局本來就沒存，補不回來）
function statsDB() {
  if (!stats.db) {
    stats.db = new Promise((resolve) => {
      try {
        const req = indexedDB.open(STATS_DB, 2);
        req.onupgradeneeded = (e) => {
          const db = req.result;
          if (e.oldVersion < 1) db.createObjectStore(STATS_STORE, { keyPath: "n" });
          if (e.oldVersion < 2) {
            const meta = db.createObjectStore(STATS_META, { keyPath: "k" });
            req.transaction.objectStore(STATS_STORE).getAll().onsuccess = (ev) => {
              const doc = emptySeries();
              ev.target.result.forEach((r) => seriesAdd(doc, r));
              meta.put(doc);
            };
          }
        };
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
    const tx = db.transaction([STATS_STORE, STATS_META], mode);
    let out;
    tx.oncomplete = () => resolve(out);
    tx.onerror = tx.onabort = () => reject(tx.error || new Error("IndexedDB 交易失敗"));
    fn(tx.objectStore(STATS_STORE), tx.objectStore(STATS_META), (v) => (out = v));
  });
}

// 在同一個交易裡先讀最後一筆與走勢資料再接著寫，多個分頁同時寫入也不會撞號；超過 STATS_KEEP 的舊紀錄一併刪掉
async function statsAppend(items) {
  const db = await statsDB();
  if (!db || !items.length) return;
  await statsTx(db, "readwrite", (st, meta) => {
    meta.get("series").onsuccess = (e1) => {
      const doc = e1.target.result || emptySeries();
      st.openCursor(null, "prev").onsuccess = (e2) => {
        const cur = e2.target.result;
        let last = cur ? cur.value : null;
        for (const item of items) {
          last = statsNext(last, item);
          st.put(last);
          seriesAdd(doc, last);
        }
        meta.put(doc);
        if (last.n > STATS_KEEP) st.delete(IDBKeyRange.upperBound(last.n - STATS_KEEP));
      };
    };
  });
}

async function statsLoad() {
  const db = await statsDB();
  if (!db) {
    stats.records = [];
    stats.series = emptySeries();
  } else {
    await statsTx(db, "readonly", (st, meta) => {
      st.getAll().onsuccess = (e) => (stats.records = e.target.result);
      meta.get("series").onsuccess = (e) => (stats.series = e.target.result || emptySeries());
    });
  }
  stats.units = statsUnits();
  stats.sel = null; // 資料換了，舊的區間索引不再對應
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
    selectBet(0, false);
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
  return statsTx(db, "readonly", (st, meta, done) => (st.openCursor(null, "prev").onsuccess = (e) => done(e.target.result ? e.target.result.value : null)));
}

async function renderFundsChip() {
  const last = await statsLast().catch(() => null);
  const v = statsFunds() + (last ? last.net : 0);
  // 破產過（歷史最低資金跌破 0）就整個標紅，不用打開面板也看得出來
  const bust = !!last && statsFunds() + last.lo < 0;
  $("funds-val").textContent = fmtNum(v);
  $("funds-chip").classList.toggle("neg", bust);
  $("funds-chip").title = bust
    ? `本次統計中破產過（最低資金 ${fmtNum(statsFunds() + last.lo)}）。按一下打開玩家統計`
    : "玩家剩餘資金：起始資金 + 累計淨輸贏（按一下打開玩家統計）";
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
  // Esc：有區間選取時先清掉選取，再按一次才關閉面板
  $("stats").addEventListener("cancel", (e) => {
    if (!stats.sel) return;
    e.preventDefault();
    stats.sel = null;
    renderSelection();
  });
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
  initChartPointer();
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
  if (db) await statsTx(db, "readwrite", (st, meta) => { st.clear(); meta.clear(); });
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
const fmtSigned = (v, digits = 0) => `${v > 0 ? "+" : v < 0 ? "−" : "±"}${fmtNum(Math.abs(v), digits)}`;

// ---------- 走勢圖的點 ----------

// 走勢圖的點：保留範圍之前用走勢資料的格子，保留範圍內每局一點（同樣的格式，a = b）。
// 從第 1 局開始累計時，最前面補一個第 0 局的點（起始資金），區間可以從開局算起
function statsUnits() {
  const recs = stats.records;
  const r0 = recs.length ? recs[0].n : Infinity;
  const units = [];
  for (const b of stats.series ? stats.series.buckets : []) {
    if (b.b >= r0) break;
    units.push(b);
  }
  recs.forEach((r) => units.push(bucketOf(r)));
  if (units.length && units[0].a === 1) {
    units.unshift({ a: 0, b: 0, net: 0, cb: 0, cw: 0, cm: 0, cf: 0, lo: 0, hi: 0, v: [[0, 0, 0, 0], [NaN, 0, NaN, 0], [NaN, 0, NaN, 0]], dd: 0, pm: 0 });
  }
  return units;
}

// 第一次資金跌破 0：lo 是到該點為止的最低淨輸贏，找第一個 funds + lo < 0 的點。
// 逐局的點是精確的局號；格子只知道發生在格內的哪一段
function statsBust(funds) {
  const units = stats.units;
  const i = units.findIndex((u) => funds + u.lo < 0);
  if (i < 0) return null;
  const u = units[i];
  return { index: i, a: u.a, b: u.b, exact: u.a === u.b };
}

// 三種視圖：一次畫一條線，都用主題綠，靠切換按鈕與圖例標明目前是哪一個。k 是 bucket.v 的索引
function statsSeries(funds) {
  if (stats.view === "rtp") return { name: "資金 RTP", hint: "總贏分 ÷ 總押注", k: 1, val: rtpOf, off: 0, refs: [100], fmt: fmtPct, rtp: true };
  if (stats.view === "mrtp") return { name: "倍率 RTP", hint: "每局贏倍的平均", k: 2, val: mrtpOf, off: 0, refs: [100], fmt: fmtPct, rtp: true };
  return { name: "資金", hint: "起始資金 + 累計淨輸贏", k: 0, val: (u) => u.net, off: funds, refs: [funds, 0], fmt: (v) => fmtNum(v) };
}

// units 中局號最接近 n 的點（units 依局號排序）
function nearestUnit(n) {
  const units = stats.units;
  let lo = 0, hi = units.length - 1;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (units[mid].b < n) lo = mid + 1;
    else hi = mid;
  }
  return lo > 0 && Math.abs(units[lo - 1].b - n) <= Math.abs(units[lo].b - n) ? lo - 1 : lo;
}

// ---------- 走勢圖 ----------

const CHART = { h: 220, l: 72, r: 14, t: 12, b: 24 };

function renderStatsChart() {
  const box = $("stats-chart");
  const units = stats.units;
  const legend = $("stats-legend");
  if (units.length < 2) {
    box.innerHTML = `<div class="stats-empty muted">至少兩局才畫走勢</div>`;
    legend.innerHTML = "";
    stats.chart = null;
    renderSelection();
    return;
  }
  const funds = statsFunds();
  const s = statsSeries(funds);
  const W = Math.max(320, box.clientWidth);
  const pw = W - CHART.l - CHART.r;
  const ph = CHART.h - CHART.t - CHART.b;
  const n0 = units[0].b, n1 = units[units.length - 1].b;
  const x = (n) => CHART.l + (pw * (n - n0)) / (n1 - n0);

  // 每個點畫成格內的最低、最高（依發生順序）與結尾；逐局的點三者相同
  const pts = [];
  units.forEach((u) => {
    const [mi, miN, ma, maN] = u.v[s.k];
    const end = s.val(u);
    if (u.a !== u.b) for (const [n, v] of miN <= maN ? [[miN, mi], [maN, ma]] : [[maN, ma], [miN, mi]]) pts.push([n, v + s.off]);
    pts.push([u.b, end + s.off]);
  });
  const shown = pts.filter(([, v]) => Number.isFinite(v));

  // RTP 在開頭幾局會劇烈跳動（第一局 ×20 就是 2000%），縱軸範圍略過開頭 5%（最多 200 局）再決定，超出的部分在圖框邊緣截掉
  const skip = s.rtp && n0 <= 1 ? Math.min(Math.floor(n1 * 0.05), 200) : -Infinity;
  let lo = Infinity, hi = -Infinity;
  shown.forEach(([n, v]) => { if (n > skip) { if (v < lo) lo = v; if (v > hi) hi = v; } });
  if (!Number.isFinite(lo)) shown.forEach(([, v]) => { if (v < lo) lo = v; if (v > hi) hi = v; });
  // 參考線只在範圍附近時納入，避免為了畫 0 把曲線壓扁
  s.refs.forEach((ref) => { const span = Math.max(hi - lo, 1e-9); if (ref >= lo - span * 0.25 && ref <= hi + span * 0.25) { lo = Math.min(lo, ref); hi = Math.max(hi, ref); } });
  if (hi - lo < 1e-9) { hi += 1; lo -= 1; }
  const padY = (hi - lo) * 0.06;
  lo -= padY;
  hi += padY;
  const y = (v) => CHART.t + ph * (1 - (v - lo) / (hi - lo));

  // 點太多時每個像素欄只留最高與最低，峰谷不會被抽樣抽掉
  let d = "";
  const put = (n, v) => (d += `${d ? "L" : "M"}${x(n).toFixed(1)},${y(v).toFixed(1)}`);
  if (shown.length <= pw * 2) shown.forEach(([n, v]) => put(n, v));
  else {
    let col = -1, mi = null, ma = null;
    const flush = () => { if (mi) (mi[0] <= ma[0] ? [mi, ma] : [ma, mi]).forEach(([n, v], i) => (i === 0 || mi !== ma) && put(n, v)); };
    shown.forEach((p) => {
      const c = Math.floor(x(p[0]) - CHART.l);
      if (c !== col) { flush(); col = c; mi = ma = p; return; }
      if (p[1] < mi[1]) mi = p;
      if (p[1] > ma[1]) ma = p;
    });
    flush();
  }

  const ticks = [hi - padY, (hi + lo) / 2, lo + padY];
  const base = CHART.t + ph;
  const xStart = x(shown[0][0]), xEnd = x(shown[shown.length - 1][0]);
  let svg = `<svg width="${W}" height="${CHART.h}" viewBox="0 0 ${W} ${CHART.h}" aria-label="走勢圖">` +
    `<defs><linearGradient id="stats-fade" class="fade" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-opacity="0.32" /><stop offset="1" stop-opacity="0" /></linearGradient>` +
    `<clipPath id="stats-clip"><rect x="${CHART.l}" y="${CHART.t}" width="${pw}" height="${ph}" /></clipPath></defs>`;
  ticks.forEach((t) => {
    svg += `<line class="grid" x1="${CHART.l}" x2="${W - CHART.r}" y1="${y(t).toFixed(1)}" y2="${y(t).toFixed(1)}" />` +
      `<text class="axis" x="${CHART.l - 8}" y="${(y(t) + 4).toFixed(1)}" text-anchor="end">${esc(s.fmt(t))}</text>`;
  });
  s.refs.forEach((ref) => {
    if (ref < lo || ref > hi) return;
    svg += `<line class="ref" x1="${CHART.l}" x2="${W - CHART.r}" y1="${y(ref).toFixed(1)}" y2="${y(ref).toFixed(1)}" />`;
  });
  // 山形圖：線下方填一層由上往下淡出的漸層
  svg += `<rect id="stats-sel" class="sel-band" x="0" y="${CHART.t}" width="0" height="${ph}" visibility="hidden" />` +
    `<path class="area" d="${d}L${xEnd.toFixed(1)},${base}L${xStart.toFixed(1)},${base}Z" fill="url(#stats-fade)" clip-path="url(#stats-clip)" />` +
    `<path class="line s-a" d="${d}" clip-path="url(#stats-clip)" />`;
  if (stats.view === "funds") {
    const bust = statsBust(funds);
    if (bust) {
      const u = units[bust.index];
      const [n, v] = bust.exact ? [u.b, u.net] : [u.v[0][1], u.v[0][0]];
      svg += `<circle class="bust-dot" cx="${x(n).toFixed(1)}" cy="${y(v + funds).toFixed(1)}" r="4.5"><title>${esc(bustText(bust))}破產</title></circle>`;
    }
  }
  svg += `<text class="axis" x="${CHART.l}" y="${CHART.h - 6}">#${fmtNum(n0)}</text>` +
    `<text class="axis" x="${W - CHART.r}" y="${CHART.h - 6}" text-anchor="end">#${fmtNum(n1)}</text>` +
    `<line id="stats-guide" class="guide" x1="0" x2="0" y1="${CHART.t}" y2="${base}" visibility="hidden" />` +
    `<circle id="stats-dot" class="guide-dot s-a" r="3.5" visibility="hidden" />` +
    `<circle id="stats-sel-a" class="sel-dot" r="4" visibility="hidden" /><circle id="stats-sel-b" class="sel-dot" r="4" visibility="hidden" />` +
    `</svg>`;
  box.innerHTML = svg;
  const coarse = units.some((u) => u.a !== u.b && u.a > 0);
  legend.innerHTML = `<span class="legend s-a"><i></i>${esc(s.name)}</span><span class="muted">${esc(s.hint)}</span>` +
    (coarse ? `<span class="muted" title="保留範圍（最近 ${fmtNum(STATS_KEEP)} 局）之前的走勢每點涵蓋多局，區間只能選在點上">早期每點 ${fmtNum(stats.series.size)} 局</span>` : "");
  stats.chart = { x, y, s, pw, n0, n1 };
  renderSelection();
}

function bustText(bust) {
  return bust.exact ? `第 ${fmtNum(bust.b)} 局` : `第 ${fmtNum(bust.a)}～${fmtNum(bust.b)} 局之間`;
}

// ---------- 滑鼠讀值與區間選取 ----------
// 滑鼠：按住就鎖定起點，拖曳顯示起點到目前位置的變化；放開後保留選取，點一下圖或按 Esc 清掉。
// 觸控：長按 0.3 秒鎖定起點再拖曳；還沒鎖定前移動就當成捲動。

function unitAtEvent(e) {
  const c = stats.chart;
  const px = e.clientX - $("stats-chart").getBoundingClientRect().left;
  const n = c.n0 + ((px - CHART.l) / c.pw) * (c.n1 - c.n0);
  return nearestUnit(Math.max(c.n0, Math.min(c.n1, n)));
}

function initChartPointer() {
  const box = $("stats-chart");
  let press = null; // { id, x, y, timer, moved }
  const begin = (e) => {
    const i = unitAtEvent(e);
    stats.sel = { i, j: i, dragging: true };
    try { box.setPointerCapture(e.pointerId); } catch {}
    renderSelection();
  };
  box.addEventListener("pointerdown", (e) => {
    if (!stats.chart || e.button !== 0) return;
    if (e.pointerType === "touch") {
      press = { x: e.clientX, y: e.clientY, timer: setTimeout(() => { press = null; begin(e); }, 300) };
      return;
    }
    e.preventDefault();
    begin(e);
  });
  box.addEventListener("pointermove", (e) => {
    if (!stats.chart) return;
    if (press && Math.hypot(e.clientX - press.x, e.clientY - press.y) > 8) {
      clearTimeout(press.timer);
      press = null;
    }
    if (stats.sel && stats.sel.dragging) {
      stats.sel.j = unitAtEvent(e);
      renderSelection();
    } else {
      statsHover(e);
    }
  });
  const end = () => {
    if (press) { clearTimeout(press.timer); press = null; }
    if (!stats.sel || !stats.sel.dragging) return;
    stats.sel.dragging = false;
    if (stats.sel.i === stats.sel.j) stats.sel = null; // 沒拖曳就是點一下：清掉選取
    renderSelection();
  };
  box.addEventListener("pointerup", end);
  box.addEventListener("pointercancel", end);
  box.addEventListener("pointerleave", () => { if (!stats.sel) statsHover(null); });
  box.style.touchAction = "pan-y";
}

// 滑鼠移到走勢圖上：顯示該點的局號與數值
function statsHover(e) {
  const c = stats.chart;
  const guide = $("stats-guide"), dot = $("stats-dot");
  if (!c || !guide || stats.sel) return;
  if (!e) {
    guide.setAttribute("visibility", "hidden");
    dot.setAttribute("visibility", "hidden");
    readout();
    return;
  }
  const u = stats.units[unitAtEvent(e)];
  const v = c.s.val(u);
  const gx = c.x(u.b).toFixed(1);
  guide.setAttribute("x1", gx);
  guide.setAttribute("x2", gx);
  guide.setAttribute("visibility", "visible");
  if (Number.isFinite(v)) {
    const cy = c.y(v + c.s.off);
    dot.setAttribute("cx", gx);
    dot.setAttribute("cy", cy.toFixed(1));
    dot.setAttribute("visibility", cy >= CHART.t && cy <= CHART.h - CHART.b ? "visible" : "hidden");
  } else dot.setAttribute("visibility", "hidden");
  readout(`<b>#${fmtNum(u.b)}</b>${u.a !== u.b && u.a > 0 ? `<span class="muted">（第 ${fmtNum(u.a)}～${fmtNum(u.b)} 局）</span>` : ""}　${esc(c.s.name)} ${Number.isFinite(v) ? esc(c.s.fmt(v + c.s.off)) : "—"}`);
}

function readout(html) {
  $("stats-readout").innerHTML = html || `<span class="muted">按住滑鼠拖曳，看兩點之間的變化</span>`;
}

// 區間 (A, B]：A、B 都是點上的累計值，所以押注、贏分、RTP、FG 次數直接相減；最大回撤與最大贏倍掃過中間的點
function rangeStats(i, j) {
  const units = stats.units;
  const A = units[i], B = units[j];
  const count = B.b - A.b;
  let peak = A.net, dd = 0, pm = 0;
  for (let k = i + 1; k <= j; k++) {
    const u = units[k];
    dd = Math.max(dd, u.dd, peak - u.v[0][0]);
    peak = Math.max(peak, u.v[0][2]);
    pm = Math.max(pm, u.pm);
  }
  const bet = B.cb - A.cb, win = B.cw - A.cw;
  return { A, B, count, bet, win, rtp: bet > 0 ? (100 * win) / bet : NaN, mrtp: count > 0 ? (100 * (B.cm - A.cm)) / count : NaN, fg: B.cf - A.cf, dd, pm };
}

function renderSelection() {
  const c = stats.chart;
  const band = $("stats-sel");
  if (!c || !band) {
    readout();
    return;
  }
  const dots = [$("stats-sel-a"), $("stats-sel-b")];
  if (!stats.sel) {
    band.setAttribute("visibility", "hidden");
    dots.forEach((d) => d.setAttribute("visibility", "hidden"));
    readout();
    return;
  }
  $("stats-guide").setAttribute("visibility", "hidden");
  $("stats-dot").setAttribute("visibility", "hidden");
  const [i, j] = stats.sel.i <= stats.sel.j ? [stats.sel.i, stats.sel.j] : [stats.sel.j, stats.sel.i];
  const r = rangeStats(i, j);
  const s = c.s;
  // 差額用顯示出來的數字相減（RTP 取到小數兩位、資金取整數），讀數上的加減才對得起來
  const digits = s.rtp ? 100 : 1;
  const shownVal = (v) => Math.round((v + s.off) * digits) / digits;
  const va = s.val(r.A), vb = s.val(r.B);
  const delta = shownVal(vb) - shownVal(va);
  const up = !(delta < 0);
  band.setAttribute("x", Math.min(c.x(r.A.b), c.x(r.B.b)).toFixed(1));
  band.setAttribute("width", Math.abs(c.x(r.B.b) - c.x(r.A.b)).toFixed(1));
  band.setAttribute("visibility", i === j ? "hidden" : "visible");
  [r.A, r.B].forEach((u, k) => {
    const v = s.val(u);
    const d = dots[k];
    if (!Number.isFinite(v)) return d.setAttribute("visibility", "hidden");
    d.setAttribute("cx", c.x(u.b).toFixed(1));
    d.setAttribute("cy", c.y(v + s.off).toFixed(1));
    d.setAttribute("visibility", "visible");
  });
  if (i === j) {
    readout(`<b>#${fmtNum(r.A.b)}</b>　拖曳選取區間`);
    return;
  }
  // 目前這條線的變化：資金寫差額與百分比，RTP 寫百分點
  let change = "—";
  if (Number.isFinite(va) && Number.isFinite(vb)) {
    const pct = s.rtp ? "" : shownVal(va) > 0 ? `，${fmtSigned((100 * delta) / shownVal(va), 2)}%` : "";
    change = `${esc(s.fmt(va + s.off))} → ${esc(s.fmt(vb + s.off))} <span class="${up ? "up" : "down"}">${s.rtp ? `${fmtSigned(delta, 2)} 個百分點` : `${fmtSigned(delta)}${pct}`}</span>`;
  }
  const item = (k, v, tip) => `<span${tip ? ` title="${esc(tip)}"` : ""}><span class="muted">${k}</span> ${v}</span>`;
  readout(
    `<b>#${fmtNum(r.A.b)} → #${fmtNum(r.B.b)}</b><span class="muted">（${fmtNum(r.count)} 局）</span>` +
    item(s.name, change) +
    item("押注", fmtNum(r.bet)) +
    item("贏分", fmtNum(r.win)) +
    item("區間資金 RTP", Number.isFinite(r.rtp) ? fmtPct(r.rtp) : "—", "區間贏分 ÷ 區間押注") +
    item("區間倍率 RTP", Number.isFinite(r.mrtp) ? fmtPct(r.mrtp) : "—", "區間每局贏倍的平均") +
    item("FG", `${fmtNum(r.fg)} 次`) +
    item("最大贏倍", `×${fmtNum(r.pm, 2)}`) +
    item("最大回撤", fmtNum(r.dd), "區間內資金從高點往後掉最多的幅度"),
  );
}

// ---------- 數字與歷史局 ----------

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
  if (bust) items.push(["破產", bustText(bust), "資金第一次跌破 0"]);
  $("stats-nums").innerHTML = items
    .map(([k, v, tip]) => `<div${tip ? ` title="${esc(tip)}"` : ""}${k === "破產" ? ` class="bust"` : ""}><span class="muted">${k}</span><b>${esc(v)}</b></div>`)
    .join("");
  renderStatsChart();
  renderStatsList();
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
