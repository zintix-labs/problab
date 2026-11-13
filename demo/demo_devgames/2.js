// demo_choice：BG 觸發後二選一。右側面板說明各入口、依 checkpoint 的 Scatter 數推算轉數，
// FG 中顯示轉數進度；觸發那一步標出 Scatter。
(() => {
  const { kv, progress, esc } = DEV.util;
  const scatters = (ctx) => (ctx.act.acttype === "trigger" && ctx.act.ext ? ctx.act.ext.scatter_hits || [] : []);

  // 入口值 → FG；轉數＝基本轉數＋（Scatter − 3）
  function entries(fixed) {
    return {
      1: { name: "FG-A", base: fixed.free_round_a, mult: 1 },
      2: { name: "FG-B", base: fixed.free_round_b, mult: fixed.multiplier_b },
    };
  }
  const rounds = (e, scatterCount) => e.base + scatterCount - 3;

  DEV.register(2, {
    title: "demo_choice 資訊",
    cellClass: (idx, ctx) => (scatters(ctx).includes(idx) ? ["mark"] : []),
    badge: (idx, ctx) => (scatters(ctx).includes(idx) ? "S" : ""),
    panel(ctx) {
      const table = entries(ctx.game.fixed);
      const seg = ctx.segment;
      let html = "";
      if (ctx.segIdx === 0) {
        const cp = seg.spin_state && seg.spin_state.cp;
        html += kv([["觸發", ctx.ext ? "是" : "尚未"], ["Scatter", ctx.ext ? ctx.ext.scatters : "—"]]);
        if (cp && seg.choose_next) {
          html += `<div class="muted">選項（Scatter ${cp.scatters} 個）</div>` + kv(seg.choose_next.map((entry, i) => {
            const e = table[entry];
            return [`#${i} 入口 ${entry}`, e ? `${e.name}：${rounds(e, cp.scatters)} 轉${e.mult > 1 ? `，贏分 ×${e.mult}` : ""}` : "未知入口"];
          }));
        }
      } else {
        const prev = ctx.bet.segments[ctx.segIdx - 1];
        const entry = prev.choose_next[seg.choice];
        const e = table[entry];
        const scatterCount = prev.spin_state.cp ? prev.spin_state.cp.scatters : 3;
        const total = e ? rounds(e, scatterCount) : 0;
        html += kv([
          ["選擇", `#${seg.choice} 入口 ${esc(entry)}（${e ? e.name : "未知"}）`],
          ["轉數推算", e ? `${e.base} ＋（Scatter ${scatterCount} − 3）＝ ${total}` : "—"],
          ["贏分倍數", e ? `×${e.mult}` : "—"],
        ]);
        if (e) html += progress(ctx.act.round + 1, total, `${e.name} 轉數`);
      }
      return html;
    },
  });
})();
