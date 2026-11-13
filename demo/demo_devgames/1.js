// demo_cascade：觸發那一步標出 Scatter；右側面板顯示觸發與 Scatter 加給、消除次數與 FG 轉數。
(() => {
  const { kv, progress } = DEV.util;
  const scatters = (ctx) => (ctx.act.acttype === "trigger" && ctx.act.ext ? ctx.act.ext.scatter_hits || [] : []);

  // 本 round 到目前這一步為止的消除次數（每次 clear 是一次消除）
  function clears(ctx) {
    const acts = ctx.mode.acts;
    const at = acts.indexOf(ctx.act);
    let n = 0;
    for (let i = 0; i <= at; i++) if (acts[i].round === ctx.act.round && acts[i].acttype === "clear") n++;
    return n;
  }

  DEV.register(1, {
    title: "demo_cascade 資訊",
    cellClass: (idx, ctx) => (scatters(ctx).includes(idx) ? ["mark"] : []),
    badge: (idx, ctx) => (scatters(ctx).includes(idx) ? "S" : ""),
    panel(ctx) {
      const fixed = ctx.game.fixed;
      const ext = ctx.ext;
      const rows = [
        ["觸發", ext ? "是" : "尚未"],
        ["Scatter", ext ? ext.scatters : "—"],
      ];
      if (ext) rows.push(["Scatter 加給", `${fixed.scatter_pay} × 押注倍數 ${ctx.segment.betmult} = ${fixed.scatter_pay * ctx.segment.betmult}`]);
      rows.push(["本 round 消除", `${clears(ctx)} 次（上限 ${fixed.max_step}）`]);
      let html = kv(rows);
      if (ctx.mode.modeid === 1) html += progress(ctx.act.round + 1, fixed.free_rounds, "FG 轉數");
      return html;
    },
  });
})();
