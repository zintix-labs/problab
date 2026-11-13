// demo_normal：觸發那一步標出 Scatter；右側面板顯示觸發資訊與 FG 轉數。
(() => {
  const { kv, progress } = DEV.util;
  // ext 只出現在 trigger 那一步：scatter_hits 是 Scatter 的格子位置
  const scatters = (ctx) => (ctx.act.acttype === "trigger" && ctx.act.ext ? ctx.act.ext.scatter_hits || [] : []);

  DEV.register(0, {
    title: "demo_normal 資訊",
    cellClass: (idx, ctx) => (scatters(ctx).includes(idx) ? ["mark"] : []),
    badge: (idx, ctx) => (scatters(ctx).includes(idx) ? "S" : ""),
    panel(ctx) {
      const ext = ctx.ext;
      let html = kv([
        ["觸發", ext ? "是" : "尚未"],
        ["Scatter", ext ? ext.scatters : "—"],
      ]);
      if (ctx.mode.modeid === 1) html += progress(ctx.act.round + 1, ctx.game.fixed.free_round, "FG 轉數");
      return html;
    },
  });
})();
