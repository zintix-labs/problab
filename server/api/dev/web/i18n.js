// ProbLab Dev 介面語言：英文（預設）與繁體中文。所有介面文字集中在 MSG，每個鍵是 [英文, 中文]；
// {name} 是代入的值。index.html 的固定文字用 data-i18n（textContent）、data-i18n-title、data-i18n-placeholder、
// data-i18n-aria（aria-label）標上鍵，切換語言時由 applyI18n 重寫；程式產生的文字用 t()。
// 外掛用 DEV.t({ en, zh }) 依目前語言取字，DEV.lang 是目前語言（見 README）。
"use strict";

const LANGS = ["en", "zh"];

const MSG = {
  // 頂部欄
  "bar.game": ["Game", "機台"],
  "bar.betmode": ["Bet mode", "押注模式"],
  "bar.betmult": ["Mult", "倍數"],
  "bar.betmult.tip": ["Bet multiplier; wager = bet unit × multiplier", "投注倍數；投注額 = 押注單位 × 倍數"],
  "bar.wager": ["Wager {v}", "投注額 {v}"],
  "bar.choose": ["Choice", "選擇方式"],
  "bar.choose.tip": ["How choice games handle a choice point", "選擇類遊戲遇到選擇點時的處理方式"],
  "bar.input": ["Input", "輸入"],
  "bar.input.tip": ["Seed: the game's root seed; Snap: the start_b64u of a Cycle", "Seed：建立機台的根 seed；Snap：某個 Cycle 的 start_b64u"],
  "bar.value.seed": ["blank = random | integer | hex:… | utf8:…", "空白隨機｜整數｜hex:…｜utf8:…"],
  "bar.value.snap": ["start_b64u of a Cycle (only Cycle 0 is guaranteed)", "某個 Cycle 的 start_b64u（只保證 Cycle 0）"],
  "bar.rounds": ["Bets", "局數"],
  "bar.seed.tip": ["Copy this batch's seed: {seed}", "複製這批的 seed：{seed}"],
  "bar.funds": ["Funds", "資金"],
  "bar.funds.tip": ["Player funds: starting funds + net result (click to open player stats)", "玩家剩餘資金：起始資金 + 累計淨輸贏（按一下打開玩家統計）"],
  "bar.funds.bust": ["Went bust this session (lowest funds {v}). Click to open player stats", "本次統計中破產過（最低資金 {v}）。按一下打開玩家統計"],
  "bar.stats": ["Player stats (S)", "玩家統計（S）"],
  "bar.stats.aria": ["Player stats", "玩家統計"],
  "bar.help": ["Shortcuts (?)", "快捷鍵（?）"],
  "bar.help.aria": ["Keyboard shortcuts", "快捷鍵說明"],
  "bar.lang": ["Language", "語言"],
  "theme.tip": ["Theme: {name} (click to switch)", "主題：{name}（按一下切換）"],
  "theme.aria": ["Theme: {name}", "主題：{name}"],
  "theme.system": ["system", "跟隨系統"],
  "theme.light": ["light", "淺色"],
  "theme.dark": ["dark", "深色"],

  // 狀態列
  "status.running": ["Running…", "執行中…"],
  "status.snap": ["snap replay", "snap 回放"],
  "status.snapPartial": ["snap replay (only Cycle 0 is guaranteed)", "snap 回放（只保證 Cycle 0）"],
  "status.clamped": ["limited to {n} bets", "已限制為 {n} 局"],
  "status.sep": [" | ", "｜"],
  "status.error": ["Error: {e}", "錯誤：{e}"],
  "status.loadFail": ["Failed to load: {e}", "載入失敗：{e}"],
  "status.pluginFail": ["Failed to load games/{gid}.js; showing the default view only", "games/{gid}.js 載入失敗，只顯示公版"],
  "status.maxSeg": ["Error: a bet has at most {n} Cycles", "錯誤：一局最多 {n} 個 Cycle"],
  "status.betEnd": ["Bet #{n} ended, total win {win}", "第 {n} 局結束，總贏分 {win}"],
  "status.pickNext": ["Choose the next Cycle", "請選擇下一個 Cycle"],
  "status.statsFail": ["Failed to save stats: {e}", "統計寫入失敗：{e}"],
  "status.noGame": ["Error: game {gid} not found", "錯誤：找不到機台 {gid}"],
  "status.replaying": ["Replaying…", "重播中…"],
  "status.replayOk": ["History bet #{n} replay (not counted in stats)", "歷史局 #{n} 重播（不計入統計）"],
  "status.replayDiff": ["⚠ History bet #{n} replayed differently from its record: the game config or code may have changed", "⚠ 歷史局 #{n} 的重播結果與紀錄不同：遊戲設定或程式可能改過"],
  "status.replayFail": ["Replay failed: {e}", "重播失敗：{e}"],

  // 說明
  "help.keyboard": ["Keyboard", "鍵盤"],
  "help.step": ["Previous / next step", "上一步／下一步"],
  "help.round": ["Previous / next round", "上一個／下一個 round"],
  "help.ends": ["First / last step", "第一步／最後一步"],
  "help.bet": ["Previous / next bet (follows the bet list filter)", "上一局／下一局（依局列表的篩選）"],
  "help.play": ["Play / pause (pausing turns Auto off)", "播放／暫停（暫停會關掉「自動」）"],
  "help.speed": ["Next speed; turbo wraps back to normal", "速度切到下一檔，turbo 之後回到 normal"],
  "help.symmode": ["Toggle SymbolName / SymbolID", "切換 SymbolName／SymbolID"],
  "help.stats": ["Open / close player stats", "開關玩家統計"],
  "help.esc": ["Close help; clear pinned cell numbers", "關閉說明；清掉釘住的格子編號"],
  "help.help": ["Show / hide this help", "開關這個說明"],
  "help.mouse": ["Mouse", "滑鼠"],
  "help.m.hover": ["Hover a cell: show its index and name / ID", "移到格子上：顯示格子編號與名稱／ID"],
  "help.m.pin": ["Click a cell: pin its index; click again to unpin", "點格子：釘住編號，再點一次取消"],
  "help.m.timeline": ["Click the timeline: jump to that step", "點時間軸：跳到那一步"],
  "help.m.speed": ["Click the n-th speed chevron: switch to that speed", "點速度的第幾個箭頭：切到第幾檔"],
  "help.m.chart": ["Drag on the stats chart: compare two points", "在統計走勢圖上拖曳：看兩點之間的變化"],

  // 局列表與篩選
  "list.title": ["Bets", "局列表"],
  "list.summary": ["{done} bets | bet {bet} | win {win} | RTP {rtp}%", "{done} 局｜總押 {bet}｜總贏 {win}｜RTP {rtp}%"],
  "list.pending": [" | {n} in progress", "｜{n} 局進行中"],
  "list.cycles": ["{n} Cycles", "{n} 個 Cycle"],
  "list.open": ["In progress", "進行中"],
  "filter.only": ["Only ≥", "只看 ≥"],
  "filter.unit": ["x", "倍"],
  "filter.tip": ["Only bets with win > 0 and a win multiple (win ÷ bet) at or above the threshold; 0 means any win", "只看贏分 > 0 且贏倍（贏分 ÷ 投注）達到門檻的局；門檻 0 等於只看有贏分"],
  "filter.aria": ["Win multiple threshold", "贏倍門檻"],

  // 控制列
  "ctrl.first": ["First step (Home)", "第一步（Home）"],
  "ctrl.prev": ["Previous step (←)", "上一步（←）"],
  "ctrl.next": ["Next step (→)", "下一步（→）"],
  "ctrl.last": ["Last step (End)", "最後一步（End）"],
  "ctrl.play": ["▶ Play", "▶ 播放"],
  "ctrl.pause": ["⏸ Pause", "⏸ 暫停"],
  "ctrl.play.tip": ["Play / pause (P); pausing turns Auto off, playing turns it back on", "播放／暫停（P）；暫停會關掉「自動」，播放會再打開"],
  "ctrl.speed": ["Speed {name} (+)", "速度 {name}（+）"],
  "ctrl.speed.aria": ["Speed", "速度"],
  "ctrl.speed.level": ["Speed {name}", "速度 {name}"],
  "ctrl.single": ["Single", "單局"],
  "ctrl.chain": ["Chain", "連續"],
  "ctrl.chain.tip": ["Single: stop after this bet; Chain: go on to the next bet in the list. Click to toggle", "單局：播完這局就停；連續：接著播局列表的下一局。按一下切換"],
  "ctrl.auto": ["Auto", "自動"],
  "ctrl.auto.on": ["Autoplay: on. Spin, switching bets and answering a player choice keep playing. Click to turn off", "自動播放：開。Spin、換局、選完 player 選項後自動接著播。按一下關閉"],
  "ctrl.auto.off": ["Autoplay: off. After Spin the board stays on step 1 until you press Play (P). Click to turn on", "自動播放：關。Spin 後停在第 1 步，按播放（P）才播。按一下開啟"],
  "ctrl.symmode.tip": ["Show symbol names or board IDs in cells and wins; click to toggle (I)", "格子與中獎明細顯示圖標名稱或盤面上的 ID，按一下切換（I）"],
  "ctrl.copyLink": ["Copy link", "複製連結"],
  "ctrl.copyLink.tip": ["Copy a URL that reproduces this step", "複製能重現這一步的網址"],
  "link.player": ["player choices are not in the URL; it only reproduces Cycle 0", "player 的選擇不在網址裡，只重現到 Cycle 0"],
  "link.snap": ["Snap only reproduces Cycle 0", "Snap 只重現到 Cycle 0"],
  "copy.done": ["Copied", "已複製"],
  "copy.prompt": ["Copy the text below", "複製下面的內容"],

  // 資訊列與盤面
  "info.empty": ["Press Spin, then pick a bet on the left.", "按 Spin 產生結果，再從左邊選一局。"],
  "info.bet": ["Bet", "押注"],
  "info.win": ["Total win", "總贏分"],
  "info.cycleOf": ["{i} (of {n})", "{i}（共 {n}）"],
  "info.trigger": [" (trigger {t})", "（觸發 {t}）"],
  "info.actwin": ["Act win", "本 act"],
  "info.stepacc": ["Step total", "step 累積"],
  "info.roundacc": ["Round total", "round 累積"],
  "board.trimmed": ["trimmed", "裁切"],
  "board.blocked": ["blocked", "封鎖"],

  // 時間軸
  "tl.win": ["win {v}", "贏分 {v}"],
  "tl.trigger": [" | trigger {t}", "｜觸發 {t}"],
  "tl.roundacc": ["round total {v}", "round 累積 {v}"],
  "tl.picked": [" | picked #{i}", "｜選 #{i}"],
  "tl.atChoice": [" | ended at a choice point", "｜結束於選擇點"],
  "tl.choose": ["Choose the next Cycle:", "選擇下一個 Cycle："],
  "tl.choice.tip": ["Position {i}, entry {e}", "位置 {i}，入口 {e}"],

  // 右側面板
  "side.wins": ["Wins this step", "本步中獎"],
  "side.none": ["None", "無"],
  "side.symbol": ["Symbol", "圖標"],
  "side.count": ["Count", "數量"],
  "side.where": ["Line / comb", "線／組合"],
  "side.winCol": ["Win", "贏分"],
  "side.comb": ["{n} comb", "{n} 組"],
  "side.game": ["Game info", "機台資訊"],
  "side.rawExt": ["Raw ext", "ext 原始資料"],
  "side.noExt": ["(no ext at this step)", "（這一步沒有 ext）"],
  "side.noPlugin": ["No plugin for this game; showing the default view only.", "這台沒有注入外掛，只顯示公版。"],
  "side.pluginErr": ["games/{gid}.js error: {e}", "games/{gid}.js 錯誤：{e}"],
  "side.cycle": ["Cycle snapshot", "本 Cycle 快照"],
  "side.cycleN": ["Cycle {i} snapshot", "Cycle {i} 快照"],
  "side.cycleOf": [" ({n} Cycles)", "（共 {n} 個 Cycle）"],
  "side.start": ["start_b64u (start snapshot)", "start_b64u（起始快照）"],
  "side.after": ["after_b64u (after the Cycle)", "after_b64u（結束後）"],
  "side.cp": ["cp (output checkpoint)", "cp（輸出的 checkpoint）"],
  "side.copyStart": ["Copy start_b64u", "複製 start_b64u"],
  "side.rerun": ["Rerun from this snapshot", "用這個快照重跑"],
  "side.rerun.tip": ["Switches the input to Snap; only Cycle 0 is guaranteed", "輸入切到 Snap，只保證 Cycle 0"],
  "side.dto": ["Bet DTO", "本局 DTO"],
  "side.replay": ["Replay this bet", "回放這局"],
  "side.replay.tip": ["Rerun every Cycle from its snapshot to reproduce the whole bet", "以每個 Cycle 的快照逐一重跑，整局完全重現"],
  "side.copyDto": ["Copy full DTO", "複製完整 DTO"],
  "side.rawDto": ["Full DTO JSON (all Cycles)", "完整 DTO JSON（所有 Cycle）"],
  "side.replayOk": ["Replay matches: all {n} Cycles are identical", "回放一致：{n} 個 Cycle 完全相同"],
  "side.replayBad": ["Replay differs at Cycle {i}", "回放不一致：Cycle {i} 不同"],
  "side.replayFail": ["Replay failed: {e}", "回放失敗：{e}"],

  // 玩家統計
  "stats.title": ["Player stats", "玩家統計"],
  "stats.funds": ["Starting funds", "起始資金"],
  "stats.reset": ["Reset", "歸零"],
  "stats.reset.tip": ["Clear all stats and bet history in this browser and start over", "清空這個瀏覽器的所有統計與歷史局，重新開始累計"],
  "stats.reset.confirm": ["Click again to reset", "再按一次確認歸零"],
  "stats.close": ["Close (S / Esc)", "關閉（S／Esc）"],
  "stats.close.aria": ["Close", "關閉"],
  "stats.chart": ["Trend chart", "走勢圖"],
  "stats.view.funds": ["Funds", "資金"],
  "stats.view.rtp": ["Funds RTP", "資金 RTP"],
  "stats.view.mrtp": ["Multiplier RTP", "倍率 RTP"],
  "stats.hint.funds": ["starting funds + net result", "起始資金 + 累計淨輸贏"],
  "stats.hint.rtp": ["total win ÷ total bet", "總贏分 ÷ 總押注"],
  "stats.hint.mrtp": ["mean win multiple per bet", "每局贏倍的平均"],
  "stats.history": ["Bet history", "歷史局"],
  "stats.historyN": ["Bet history (last {n})", "歷史局（最近 {n} 局）"],
  "stats.note": ["Stats are kept in this browser and shared by all dev tabs. Only manual Spins with a Seed input count; bets opened from links, Snap and replays don't.", "統計存在這個瀏覽器，所有 dev 分頁共用一份；只計入手動按 Spin 的 Seed 局，從連結打開、Snap 與回放的局不計入。"],
  "stats.empty": ["No records yet. Press Spin with a Seed input to start.", "還沒有紀錄。在 Seed 模式按 Spin 後開始累計。"],
  "stats.noIdb": ["IndexedDB is unavailable in this browser; stats are disabled.", "這個瀏覽器無法使用 IndexedDB，統計停用。"],
  "stats.chartEmpty": ["At least two bets are needed to draw a trend", "至少兩局才畫走勢"],
  "stats.coarse": ["early points: {n} bets each", "早期每點 {n} 局"],
  "stats.coarse.tip": ["Before the retained range (last {keep} bets) each point covers several bets; ranges can only start and end on points", "保留範圍（最近 {keep} 局）之前的走勢每點涵蓋多局，區間只能選在點上"],
  "stats.bustAt": ["bet #{n}", "第 {n} 局"],
  "stats.bustBetween": ["between bets #{a} and #{b}", "第 {a}～{b} 局之間"],
  "stats.bustDot": ["Went bust at {where}", "{where}破產"],
  "stats.span": [" (bets {a}–{b})", "（第 {a}～{b} 局）"],
  "stats.idle": ["Press and drag to compare two points", "按住滑鼠拖曳，看兩點之間的變化"],
  "stats.drag": ["Drag to select a range", "拖曳選取區間"],
  "stats.count": [" ({n} bets)", "（{n} 局）"],
  "stats.pct": [", {v}%", "，{v}%"],
  "stats.pp": ["{v} pp", "{v} 個百分點"],
  "stats.r.bet": ["Bet", "押注"],
  "stats.r.win": ["Win", "贏分"],
  "stats.r.rtp": ["Range funds RTP", "區間資金 RTP"],
  "stats.r.rtp.tip": ["range win ÷ range bet", "區間贏分 ÷ 區間押注"],
  "stats.r.mrtp": ["Range multiplier RTP", "區間倍率 RTP"],
  "stats.r.mrtp.tip": ["mean win multiple per bet in the range", "區間每局贏倍的平均"],
  "stats.r.fg": ["FG", "FG"],
  "stats.times": ["{n}", "{n} 次"],
  "stats.r.pm": ["Max multiple", "最大贏倍"],
  "stats.r.dd": ["Max drawdown", "最大回撤"],
  "stats.r.dd.tip": ["largest drop in funds from a high within the range", "區間內資金從高點往後掉最多的幅度"],
  "stats.n.spins": ["Spins", "轉數"],
  "stats.n.bet": ["Total bet", "總押注"],
  "stats.n.win": ["Total win", "總贏分"],
  "stats.n.mrtp.tip": ["mean of per-bet win multiples (win ÷ that bet)", "每局贏倍（贏分 ÷ 該局押注）的平均"],
  "stats.n.fg": ["FG entries", "進 FG"],
  "stats.n.fgv": ["{n} (1/{avg})", "{n} 次（1/{avg}）"],
  "stats.n.fg.tip": ["bets that entered FG; in brackets: one in every N bets", "進過 FG 的局數；括號是平均幾局一次"],
  "stats.n.funds": ["Funds", "目前資金"],
  "stats.n.hi": ["Highest funds", "最高資金"],
  "stats.n.lo": ["Lowest funds", "最低資金"],
  "stats.n.mx": ["Max multiple", "最大贏倍"],
  "stats.n.bust": ["Bust", "破產"],
  "stats.n.bust.tip": ["first time funds dropped below 0", "資金第一次跌破 0"],
  "stats.row.tip": ["Win {win} | multiplier {m} | {c} Cycles", "贏分 {win}｜倍數 {m}｜{c} 個 Cycle"],
  "stats.row.bet": ["Bet {v}", "押 {v}"],
  "stats.row.open": ["New window", "開新視窗"],
  "stats.more": ["Show more ({n} left)", "顯示更多（還有 {n} 局）"],
  "stats.noMatch": ["No bets match the filter", "沒有符合篩選的局"],
  "stats.none": ["No records", "沒有紀錄"],
};

const i18n = { lang: "en" };

// 目前語言的文字；中文缺的鍵用英文，鍵不存在時回傳鍵本身，畫面上一眼看得出漏了哪個
function t(key, vars) {
  const m = MSG[key];
  let s = m ? m[LANGS.indexOf(i18n.lang)] ?? m[0] : key;
  if (vars) s = s.replace(/\{(\w+)\}/g, (all, k) => (vars[k] !== undefined ? vars[k] : all));
  return s;
}

// 把 index.html 上標了 data-i18n* 的元素換成目前語言
function applyI18n(root = document) {
  root.querySelectorAll("[data-i18n]").forEach((el) => (el.textContent = t(el.dataset.i18n)));
  root.querySelectorAll("[data-i18n-title]").forEach((el) => (el.title = t(el.dataset.i18nTitle)));
  root.querySelectorAll("[data-i18n-placeholder]").forEach((el) => (el.placeholder = t(el.dataset.i18nPlaceholder)));
  root.querySelectorAll("[data-i18n-aria]").forEach((el) => el.setAttribute("aria-label", t(el.dataset.i18nAria)));
  document.documentElement.lang = i18n.lang === "zh" ? "zh-Hant" : "en";
  document.querySelectorAll("#lang button").forEach((b) => b.setAttribute("aria-pressed", String(b.dataset.lang === i18n.lang)));
}

// 外掛的文字：字串照原樣，{ en, zh } 依目前語言取，缺的語言用 en，再沒有就取第一個
function pickLang(v) {
  if (v === null || v === undefined || typeof v !== "object") return v;
  return v[i18n.lang] ?? v.en ?? Object.values(v)[0];
}
