// demo_entry：BG 觸發後二選一；FG-A 再觸發時只能進 FG-B，FG-B 再觸發時只能進 FG-A。
// 右側面板說明各入口、顯示這局一路選過的入口與 FG 轉數進度；觸發那一步標出 Scatter。
(() => {
  const { kv, progress, esc } = DEV.util;
  const scatters = (ctx) => (ctx.act.acttype === "trigger" && ctx.act.ext ? ctx.act.ext.scatter_hits || [] : []);

  // 入口值 → FG；轉數固定，不隨 Scatter 數變化
  function entries(fixed) {
    return {
      3: { name: "FG-A", rounds: fixed.free_round_a, mult: 1 },
      7: { name: "FG-B", rounds: fixed.free_round_b, mult: fixed.multiplier_b },
    };
  }
  // 第 i 段（i > 0）實際進入的入口
  const entryOf = (bet, i) => bet.segments[i - 1].choose_next[bet.segments[i].choice];

  DEV.register(3, {
    title: "demo_entry 資訊",
    cellClass: (idx, ctx) => (scatters(ctx).includes(idx) ? ["mark"] : []),
    badge: (idx, ctx) => (scatters(ctx).includes(idx) ? "S" : ""),
    panel(ctx) {
      const table = entries(ctx.game.fixed);
      const name = (entry) => (table[entry] ? table[entry].name : `入口 ${entry}`);
      // 入口歷程：BG → FG-A → FG-B …，目前這一段加粗
      const path = ctx.bet.segments.map((_, i) => {
        const label = i === 0 ? "BG" : name(entryOf(ctx.bet, i));
        return i === ctx.segIdx ? `<b>${esc(label)}</b>` : esc(label);
      });
      let html = kv([
        ["入口歷程", path.join(" → ")],
        ["觸發", ctx.ext ? "是" : "尚未"],
        ["Scatter", ctx.ext ? ctx.ext.scatters : "—"],
      ]);
      const seg = ctx.segment;
      if (seg.choose_next && seg.choose_next.length) {
        html += `<div class="muted">選項</div>` + kv(seg.choose_next.map((entry, i) => {
          const e = table[entry];
          return [`#${i} 入口 ${entry}`, e ? `${e.name}：${e.rounds} 轉${e.mult > 1 ? `，贏分 ×${e.mult}` : ""}` : "未知入口"];
        }));
      }
      if (ctx.segIdx > 0) {
        const e = table[entryOf(ctx.bet, ctx.segIdx)];
        if (e) html += progress(ctx.act.round + 1, e.rounds, `${e.name} 轉數${e.mult > 1 ? `（贏分 ×${e.mult}）` : ""}`);
      }
      html += `<div class="muted">再觸發：FG-A 之後只能進 FG-B，FG-B 之後只能進 FG-A。</div>`;
      return html;
    },
  });
})();
