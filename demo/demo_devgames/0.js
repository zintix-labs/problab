// demo_normal：觸發那一步標出 Scatter，並示範 label：中央改寫成 ★，原圖標名稱移到角標；右側面板顯示觸發資訊與 FG 轉數。
// 面板文字用 DEV.t({ en, zh }) 依介面語言取。
(() => {
  const { kv, progress, sym } = DEV.util;
  const T = (en, zh) => DEV.t({ en, zh });
  // ext 只出現在 trigger 那一步：scatter_hits 是 Scatter 的格子位置
  const scatters = (ctx) => (ctx.act.acttype === "trigger" && ctx.act.ext ? ctx.act.ext.scatter_hits || [] : []);

  DEV.register(0, {
    title: { en: "demo_normal info", zh: "demo_normal 資訊" },
    modes: { 0: "BaseGame", 1: "FreeGame" },
    // label 改寫的格子帶 labeled class；★ 在等寬字型裡偏小，這台放大
    css: ".cell.labeled { font-size: calc(var(--cell, 64px) * 0.42); }",
    cellClass: (idx, ctx) => (scatters(ctx).includes(idx) ? ["mark"] : []),
    label: (idx, ctx) => scatters(ctx).includes(idx) && "★",
    badge: (idx, ctx) => (scatters(ctx).includes(idx) ? sym(ctx, ctx.screen[idx]) : ""),
    panel(ctx) {
      const ext = ctx.ext;
      let html = kv([
        [T("Triggered", "觸發"), ext ? T("yes", "是") : T("not yet", "尚未")],
        ["Scatter", ext ? ext.scatters : "—"],
      ]);
      if (ctx.mode.modeid === 1) html += progress(ctx.act.round + 1, ctx.game.fixed.free_round, T("FG spins", "FG 轉數"));
      return html;
    },
  });
})();
