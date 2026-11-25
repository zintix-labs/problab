🌐 Language: En | [中文](README_ch.md)

# ProbLab Dev

Step through a whole bet in the browser, cycle by cycle and step by step: the board, winning positions, the win of every step, each cycle of a choice game, and each game's own details (marks, progress bars, badges and so on). It is a development tool for debugging and reproducing results, not for statistics; use `make run` or `/v1/sim` for those.

In this document a "cycle" is the result of one `Machine.Spin` call (one SpinResult). A normal game has one cycle per bet; a choice game splits a bet into several cycles at its choice points, and each choice sends a continuation request. The panel always labels them Cycle, matching the DTO's `cycle` field.

> The dev routes are registered only in `ModeDev` and `ModePanel` and have no authentication. **Do not expose them publicly.**

## Starting

```sh
make dev   # same as go run ./cmd/dev; opens http://localhost:5808/dev
go run ./cmd/dev -addr :5810 -open=false   # another port, no browser (e.g. to run two at once)
go run ./cmd/dev -lang zh                  # Chinese as the default UI language
```

Press Ctrl+C to stop; open connections get up to 5 seconds.

`make dev` starts in `svrcfg.ModePanel`: only the landing page and `/dev` are mounted, there is no `/v1`, and no machine pools are built. The engine is assembled with `problab.New`, `RegisterAll` and `Freeze`, so each game's optimal data is loaded on its first Spin instead of all at once at startup. Use `make svr` (`ModeDev`) when you also need `/v1/*`.

The common frontend is embedded with `go:embed` from `server/api/dev/web/`; restart after changing it.

## Using the panel

At the right end of the top bar, "EN / 中" switches the UI language and the browser remembers the choice. Until you pick one, the panel uses the default set by the application (`svrcfg.SvrCfg.DevLang`, `devcli.Options.Lang` or `cmd/dev -lang`; only `en` and `zh` are accepted), and English when none is set. To its left, the keyboard icon (or `?`) lists every shortcut and mouse action; next is the theme button, which cycles through system, light and dark; then the chart button (or `S`), which opens the player stats; and leftmost, "Balance" shows the player's current balance and also opens the player stats.

| Key | Action |
| --- | --- |
| Space / Enter | Spin |
| ← / → | Previous / next step |
| Shift+← / → | Previous / next round |
| Home / End | First / last step |
| ↑ / ↓ | Previous / next bet: moves only between bets visible in the bet list (with "Only ≥ X x" checked, bets below the threshold are skipped) and does not wrap |
| P | Play / pause (pausing turns Auto off) |
| + | Next speed; turbo wraps back to normal |
| I | Toggle SymbolName / SymbolID |
| S | Open / close the player stats; while they are open only S and Esc work |
| Esc | Closes the help if it is open, otherwise clears all pinned cell numbers |
| ? | Show / hide the help |

Keys are not intercepted while typing in inputs and selects; Enter in an input also runs Spin.

Cell numbers are hidden until you hover a cell or click it to pin the number (click again to unpin).

"Only ≥ X x" above the bet list, when checked, lists only bets with a win above 0 and a win multiple (win ÷ bet, the number in brackets in the list) of at least X. X defaults to 0.01; 0 means any win. Changing the threshold checks the box, and the browser remembers both. The bet selected after Spin, ↑↓ and Chain playback all follow this filter.

The controls under the board have three groups: stepping on the left (⏮ ◀ current step ▶ ⏭), playback in the middle (Play, speed, Single / Chain, Auto), and view and sharing on the right (SymbolName / SymbolID, Copy link). Two-state buttons show their current value; click to toggle.

There are three fixed speeds: normal (default, 1350 ms per step), fast (650 ms) and turbo (200 ms). Changing steps plays a drop animation on the board (see "Drop animation" below), shortened for fast and skipped for turbo, and also skipped when the system asks for reduced motion. The number of lit chevrons under the board is the current speed; click the n-th chevron to switch to that speed. Single (the default) plays only the current bet; Chain goes on to the next bet in the list (following "Only ≥ X x" as well) and stops at the last bet or at a bet waiting at a player choice. It never spins by itself.

Autoplay is on by default, like playing for real: after Spin, playback starts from step 1 of the first bet, stops and waits at the end, and the next Spin plays again. While playing, clicking another bet in the list or pressing ↑↓ continues from step 1 of that bet; a player bet waiting at a choice point continues once you choose. Manual stepping (⏮ ◀ ▶ ⏭, ←→, clicking the timeline) only stops the current bet's playback; the next Spin or bet switch plays automatically again. The Auto button next to Play is green while on; click it to toggle, even before the first Spin. When it is off, Spin only shows step 1 and Play starts playback. Pausing (or P) while playing also turns Auto off, and Play while stopped turns it back on, restarting from step 1 if already at the last step. The browser remembers the setting. Spins and replays loaded from a URL never autoplay; they stay on the step given in the URL.

The SymbolName / SymbolID button under the board (or I) chooses whether cells and the symbol column of "Wins this step" show names (`H1`) or board IDs (the index into that mode's `symbols`, i.e. the numbers in the backend array). In ID mode, empty and non-existent cells also show their raw values for cell-by-cell comparison. Hovering a cell always shows both, and the browser remembers the choice. The timeline below lists every act grouped as Cycle > State > Round; click one to jump there. Bets with a single cycle do not show the Cycle level. Mode headings read `State 1 · ModeID 2 [FreeGame]`: State is the position in that cycle's `gamemodes` (counted from 0 in each cycle), ModeID is the index into the config's `game_mode_settings`, and the name in brackets comes from the plugin; it is omitted without a plugin or when the plugin gives none. The State field of the info bar uses the same format.

## Top bar

| Field | Description |
| --- | --- |
| Game, Bet mode | Loaded from `/dev/meta` |
| Mult | Bet multiplier 1–100, default 1; the wager (bet unit × multiplier) is shown next to it. The game logic multiplies wins by it, and continuations and replays reuse the same multiplier |
| Choice | What a choice game does at a choice point: `first` (default, position 0), `last` (the last position) or `player` (stop at the choice point and let you pick) |
| Input | `Seed` or `Snap`, stated explicitly; the backend never guesses from the value |
| Value | Seed: blank for an automatic seed, a decimal integer (int64), `hex:…` or `utf8:…`; any other string without a prefix is rejected. Snap: a cycle's `start_b64u` |
| Bets | 1–1000 for Seed (larger values are clamped to 1000); always 1 for Snap and player |

### Seed and Snap

| | Seed | Snap |
| --- | --- | --- |
| What it is | The root seed material the machine is built from | The RNG starting point a cycle actually used (`start_b64u` in the result) |
| Reproducibility | The same (game, bet mode, multiplier, choice, seed, bets) always gives the same results, including optimal and choice games | Only cycle 0 is guaranteed; later cycles continue according to the choice setting and may differ from the original bet, which the panel points out |

After Spin, a "seed" button appears next to Spin. Click it to copy the seed actually used for the batch (hover to see the value) and paste it back to reproduce the batch; with a blank Seed this is how you recover the generated seed.

## Replay

| Method | How | Scope |
| --- | --- | --- |
| Replay this bet | The button under "Bet DTO" on the right: reruns every cycle from its recorded `start_b64u`, plus the continuations' `choice`, `choose_next` and `cp`, and compares the results | Reproduces the whole bet, including continuations that draw from optimal by entry |
| Rerun from this snapshot | The button under "Cycle 0 snapshot" on the right (cycle 0 only): switches the input to Snap, fills in `start_b64u` and runs | Cycle 0 only |
| Paste a Snap by hand | Paste a bet's cycle-0 `start_b64u` from the production backend's records | Cycle 0 only |

When a bet has several cycles, a single snapshot cannot restore the continuations' inputs, so a full reproduction needs "Replay this bet".

"Cycle N snapshot" on the right shows the `start_b64u`, `after_b64u` and `cp` of the cycle the current step belongs to and follows you to other cycles; "Bet DTO" holds the whole bet's replay, copy and full JSON.

## Player mode

Choose `player` and press Spin. When a bet stops at a choice point, the options appear at the end of the timeline (`#position → entry`). Clicking one sends a continuation request, and the new cycle is appended to the same bet; the bet counts toward the totals on the left only once it ends. Continuations use the same format as production and the server keeps no state. A continuation's starting point comes from the game's RNG policy, and every cycle records its own `start_b64u`, so "Replay this bet" works afterwards as well.

## Player stats

Bets from manual Spins add up to one player session across games, bet modes and multipliers until you press Reset, so you can test a player's strategy across games and modes.

- **Which bets count**: only bets from a manual Spin on the page (the button, Space or Enter) with a Seed input. Spins run from a URL (`spin=1`), history replay links, Snap and "Replay this bet" all reproduce known bets and do not count. A player bet counts once it ends; one left unfinished by another Spin does not.
- **Numbers**: spins, total bet, total win, balance RTP, multiplier RTP, FG entries (in brackets: one in every N bets), current / highest / lowest balance and max multiple; the first time the balance drops below 0, the bust bet is shown as well.
- **Two RTPs**: balance RTP is total win ÷ total bet; multiplier RTP is the mean of each bet's win multiple (win ÷ that bet). For example, two bets of 20 winning 0 and 100 winning 120 give a balance RTP of 100% and a multiplier RTP of (0 + 1.2) ÷ 2 = 60%. They are equal when the wager never changes.
- **Balance**: the starting balance defaults to 10000, can be changed, and is remembered by the browser. Balance = starting balance + net result, and the top bar always shows the current balance. Once the session has gone bust (lowest balance below 0), the number turns red and stays red even after the balance recovers; its tooltip gives the lowest balance. Changing the starting balance shifts the whole curve and the highest / lowest balance; the balance can go negative.
- **Trend chart**: an area chart with a gradient fading out under the line. It draws one line at a time, switchable between Balance, Balance RTP and Multiplier RTP, with the line's formula next to the legend. It covers the whole session, not just the 10,000 retained bets: one point per bet within the retained range and, before that, points from the trend data that cover several bets each ("early points: N bets each" next to the legend). Ranges can only start and end on points, but the values at the points are exact. Dashed lines mark the starting balance and 0 (Balance) or 100% (RTP); a red dot marks the bust. RTP swings wildly over the first bets, so the y-range skips the first 5% of bets (at most 200) and anything beyond it is clipped at the chart edge. Hovering shows the value of a point.
- **Ranges**: press and drag on the trend chart (on touch, long-press for 0.3 s and drag) and the readout shows the change between the two points: bet numbers and count, the current line's change from start to end (difference and percentage for balance, percentage points for RTP), range bet, range win, range balance RTP, range multiplier RTP, FG entries in the range, the range's max multiple and max drawdown (the largest drop in balance from a high within the range). The selection is a light band on the chart; in the readout, a rise is green and a fall is red. The selection stays after you release; click the chart or press Esc to clear it (with a selection, Esc clears it first and a second Esc closes the panel).
- **Bet history**: the last 10,000 bets, newest first, with bet number, game, BetMode, FG, bet and win multiple, filterable with "Only ≥ X x" (separate from the bet list filter). "New window" replays the bet in a new tab through a replay link, and "Copy link" copies the same URL. Beyond 10,000 bets only the oldest records are dropped; the numbers and the trend chart still cover the whole session. If the bust happened before the retained range, only the trend point it falls in is known, shown as "between bets #a and #b".
- **Reset**: click twice (the first click asks for confirmation) to clear all stats and bet history and start over.

Stats live in the browser's IndexedDB and survive reloads. All dev tabs in the same browser share one copy, so spinning in several tabs at once interleaves their records. Without IndexedDB the stats are disabled and everything else works. Each bet stores its whole replay input (every cycle's `start_b64u`, plus `choice`, `choose_next` and `cp` for continuations) and a hash of the result, not the board, so 10,000 bets take a few MB. The trend data is stored separately in at most 2048 buckets; when full, neighbouring buckets merge and each covers twice as many bets, so it stays at a few hundred KB however long you play. Each bucket keeps the cumulative values at its end, its highs and lows, its max drawdown and its max multiple, so merging never flattens peaks, troughs or drawdowns. When upgrading from an older version, the trend data can only be rebuilt from the records retained at that time. After the game config or code changes, an old record may replay differently from what was recorded, and the status line shows a warning.

## URL parameters

```
http://localhost:5808/dev?gid=2&betmode=0&betmult=1&choose=first&input=seed&value=5&rounds=300&spin=1&bet=137&step=6
```

With `spin=1` the page spins right after loading and then jumps to step `step` of bet `bet` (both counted from 1). The same parameters always open the same view (Snap input guarantees cycle 0 only). Add `symmode=id` to open in ID mode.

History replay links carry the whole bet's replay input instead, run only that bet, and reproduce player choices exactly:

```
http://localhost:5808/dev?gid=2&betmode=0&betmult=1&replay=<replay input>&h=<result hash>&n=18&step=3
```

`replay` is the JSON of every cycle's replay input encoded as base64url, `h` is the result hash at recording time (the status line warns when the replay differs), `n` is the bet number in the session, and `step` is optional. On a page opened from such a link, "Copy link" produces the same kind of URL.

"Copy link" under the board turns the current step into the URL above (filling in the seed actually used when Seed was blank), for bug reports. Player choices are not in the URL, so player bets reproduce only cycle 0; for those and for multi-cycle bets from a Snap input, a ⚠ appears next to the button with an explanation in its tooltip.

## Limits

| Item | Limit |
| --- | --- |
| Bets per batch | 1–1000 |
| Bet multiplier | 1–100 |
| Cycles per bet | `problab.DefaultMaxSegments` |
| Request body size | 1 MB |

A batch is returned only once it is fully computed and lives only in browser memory. The browser also remembers the last game, SymbolName / SymbolID, theme, UI language, autoplay, win filters and starting balance; the player stats records live in IndexedDB (see "Player stats").

## API

| Method and path | Purpose |
| --- | --- |
| `GET /dev/meta` | Game list: gid, name, logic, bet units, max win, `fixed`, each mode's columns, rows and symbol names, and whether a plugin is injected (`plugin`); plus `max_rounds`, `max_segments`, `max_bet_mult`, whether a theme is injected (`theme`) and the default UI language (`lang`). Board generation, paytable and win evaluation settings are never sent |
| `POST /dev/spin` | `{gid, betmode, betmult, choose, input, value, rounds}` → `{seed, betmult, rounds, clamped, partial_replay, total_bet, total_win, rtp, bets}`; each bet is `{index, bet, win, isend, segments}`, where `segments` holds every cycle's SpinResult |
| `POST /dev/next` | A player continuation: `{gid, betmode, betmult, cycle, choice, choose_next, cp}` → `{segment}` |
| `POST /dev/replay` | `{gid, betmode, betmult, segments: [{start_b64u, cycle, choice, choose_next, cp}]}` → `{bet}` |

- `betmult` defaults to 1; continuations and replays must use cycle 0's value. A bet's wager is bet unit × multiplier.
- A bet's win is the sum of its cycles' wins; the bet is counted once, from cycle 0. `total_bet`, `total_win` and `rtp` count only finished bets.
- `choose` and `input` are required; unknown fields and malformed values return 400 with a plain-text error.
- The board `screen` is row-major: `idx = row × columns + col`; a symbol ID is the index into that mode's `symbols`.

## Game plugins

Each game can have a `<gid>.js` that draws what is specific to it. Problab ships no game plugins: plugins are always injected by the application through `svrcfg.SvrCfg.DevGames`, and games without one show the common panel only (board, wins, timeline and DTO as usual; ext only as raw JSON). The plugins for demos 0–3 live in `demo/demo_devgames/`, are injected by `cmd/dev`, and serve as examples. See "Using it in your own project" below for injecting plugins.

```js
DEV.register(2, {
  title: { en: "demo_choice info", zh: "demo_choice 資訊" }, // right panel title; a string or { en, zh }
  modes: { 0: "BaseGame", 1: "FG-A", 2: "FG-B" }, // name of each ModeID, shown in the timeline and info bar; a string or { en, zh }
  css: ".cell.blast { outline-color: red; }", // styles for this game, optional
  panel(ctx) { return "<html>"; },     // right panel content
  cellClass(idx, ctx) { return []; },  // classes to add to a cell, e.g. "mark", "dim" or your own
  label(idx, ctx) { return ""; },      // text in the middle of a cell; an empty string keeps the default
  badge(idx, ctx) { return ""; },      // small text in a cell's bottom-right corner
  actLabel(act, ctx) { return act.acttype; }, // text for each step on the timeline
  motion(ctx) { return DEV.util.motion(ctx); }, // the board's drop animation, see "Drop animation" below
});
```

Every field is optional. `ctx` has:

| Field | Content |
| --- | --- |
| `game` | This game's data from `/dev/meta` (including `fixed` and `modes`) |
| `bet` | The bet: `{index, bet, win, isend, segments}` |
| `segment`, `segIdx` | The cycle (SpinResult) of the current step and its number |
| `mode`, `modeIdx` | The current game mode (`modeid`, `trigger`, `win`, `acts`) and its index in this cycle's `gamemodes` (the State on screen) |
| `act` | The current step (`acttype`, `round`, `step`, the various wins, `screen`, `details`, `ext`) |
| `ext`, `prevExt` | The ext of this and the previous step; when this step has none, the latest one in the bet is used |
| `screen`, `prevScreen`, `prevAct` | The board and act of this and the previous step; steps without a board reuse the previous board of the same mode |
| `modeInfo`, `symbols` | This mode's columns, rows and symbol names |

`label` is for games whose cells carry a value more important than the symbol name, such as cash or multipliers: return the amount as the big text in the middle and move the symbol name to the corner with `badge`.

- A non-empty string replaces the symbol name in the middle; `""`, `null`, `undefined`, `false` or no `label` keep the default (Z empty cells show no name). Numbers are converted to strings.
- Like `badge`, the text is shown as plain text; `<` and `&` do not become HTML.
- ID mode (SymbolID, `symmode=id`) never calls `label` and always shows raw IDs; ID mode is for comparing with the backend array cell by cell and cannot be overridden by plugins.
- Negative cells (non-existent cells) never call `label`.
- `data-sym`, the tooltip (idx, name, id), colors and overrides all follow the original symbol; `cellClass`, `badge`, win outlines and animations are unaffected.
- Relabelled cells get the `labeled` class. The font size matches the symbols, 0.28 of the cell width, and long text shrinks automatically to fit the cell (CJK and full-width characters count as 1.7 half-width characters), so you never have to handle length yourself.
- To adjust, write it in `css`: `.cell.labeled { --label-size: 0.42; }` changes the base size and long labels still shrink; setting `font-size` directly takes full control and turns the automatic shrinking off. To adjust only some cells, add your own class with `cellClass` and style that class. demo 0's plugin enlarges its ★ with `--label-size`.
- The old `long` class is gone; `.cell.labeled.long` rules in plugin css no longer apply.

`modes` and `actLabel` name game modes and actions, which is presentation work and does not belong in the game config. When writing a plugin (including with an AI assistant), first list each ModeID's columns, rows, symbols and trigger, have the user decide every ModeID's name, and offer suggestions based on that information (e.g. `FreeGame` for the mode an FG trigger leads to); ask before writing `actLabel` texts too.

UI language: plugins do not have to handle languages. A plugin written in one language works as usual and shows the same text in both. To support both languages:

- `DEV.lang` is the current language (`"en"` or `"zh"`), and `DEV.t({ en, zh })` picks the text for it, falling back to `en`; strings are returned unchanged.
- The values of `title` and `modes`, and the return values of `panel`, `badge`, `label` and `actLabel`, may be `{ en, zh }` objects, and the common panel picks the language; use `DEV.t` inside functions when building strings.
- Switching languages redraws the whole panel; plugins need not do anything.
- If a plugin may run on an older problab without the language switch, start with `const T = DEV.t || ((v) => v.en);` and use `T({ en, zh })`: older versions have no `DEV.t`, and calling it directly would stop the board from drawing.

The plugins for demos 0–3 show the bilingual style.

Helpers: `DEV.util.kv(rows)` (a two-column table), `DEV.util.progress(value, total, label)` (a progress bar), `DEV.util.sym(ctx, id)` (symbol ID to name), `DEV.util.motion(ctx)` (the common panel's inferred drop animation) and `DEV.util.esc(s)`.

### Drop animation

When the step changes, the common panel compares this step's board with the previous one and infers how cells move. It does not look at `acttype`, so games without a plugin get it too:

| Case | Animation |
| --- | --- |
| The first board of each round | The whole board drops in from above, column by column from left to right, fading in as it falls |
| A column had cells removed in the previous step, and the remaining symbols keep their order and all end up at the bottom of the column | The remaining symbols slide down from their rows and new symbols drop in from the top edge of the board. Affected columns fall together; unchanged columns stay still |
| Other changes (transforming symbols, sticky cells, sideways falls and so on) | No drop, only the glow of new symbols |

"Removed cells" are the empty cells of the previous step (Z symbols, e.g. the board after a `clear`); when the previous step has none, its winning cells are used instead, so games that clear, drop and refill within one act work too. Negative cells (non-existent cells) stay still and symbols can fall past them, as with `ops.Gravity`. Old symbols that slide down do not get the new-symbol glow; falling cells start their glow and the win outline's pop only after they land.

Games with different rules specify it with the plugin's `motion(ctx)`, called only when the step changes:

| Return value | Effect |
| --- | --- |
| `false` | No drop for this step |
| `undefined`, `null` | Use the common panel's inference |
| An object or `Map`, `idx` → `{dx, dy, order, fade}` | Animate only the listed cells |

- `dx`, `dy`: how many cells the start is away from the end, fractions allowed; negative `dy` is above and negative `dx` is to the left. Defaults to 0.
- `order`: the drop order, 0 first and higher later; the actual interval scales with the speed, and the whole stagger stays within 350 ms at normal and 180 ms at fast. Defaults to 0.
- `fade`: a new symbol that fades in while moving and gets the usual new-symbol glow; when omitted, the cell is an old symbol moving, without the glow.

For example, a game where symbols fall to the left can take the common result and change its direction, or compute everything itself:

```js
motion(ctx) {
  if (ctx.act.acttype !== "slide_left") return DEV.util.motion(ctx);
  const cols = ctx.modeInfo.columns;
  const m = new Map();
  ctx.act.ext.moves.forEach(([from, to]) => m.set(to, { dx: (from % cols) - (to % cols) }));
  return m;
},
```

## Custom colors

All panel colors are CSS variables in `app.css`. The application can inject a `theme.css` to replace the defaults, or change only one game or one symbol; variables you do not override keep their defaults.

### Default colors

| Symbol | Style |
| --- | --- |
| H | Solid background from the tone sequence by number |
| L | A light background mixing the tone into the card color, with text in a deeper tone |
| W | Off-white background, gold outline, dark gold text |
| C | White background, cyan outline, cyan text |
| S | Pale yellow background, slate outline, dark gray text |
| Z (empty) | No name and no fill, only a very faint outline |
| Others | A fixed gray |
| Negative (-1 trimmed by Trim, cells blocked by the game) | Non-existent cells: no fill and no outline, only a faint ×, so the board outline is the real shape; the tooltip shows the raw value |

The H and L tone sequence 1→6 is red, orange, purple, blue, green, gray: warm to cool to gray, like rarity from high to low. From 7 on it starts again from 1, faded. Special symbols (W, C, S) have a 2px outline that stays when they win.

### Overridable variables

| Variable | Purpose | Light default | Dark default |
| --- | --- | --- | --- |
| `--tone-1`–`--tone-6` | H and L tone sequence | `#ef4444`, `#ff8000`, `#a855f7`, `#3b82f6`, `#65a30d`, `#64748b` | Same |
| `--tone-1-text`–`--tone-6-text` | H text color | All `#fff` | Same |
| `--sym-l-bg` | Share of the tone in L's light background; the rest is the card color | `16%` | `22%` |
| `--sym-l-text` | Share of the tone in L's text color; the rest is `--sym-l-ink` | `75%` | `80%` |
| `--sym-l-ink` | The color mixed into L's text | `#000` | `#fff` |
| `--sym-w`, `--sym-w-text`, `--sym-w-ring` | W background, text, outline | `#f5f5f4`, `#a16207`, `#ca8a04` | `#e7e5e4`, `#92400e`, `#ca8a04` |
| `--sym-c`, `--sym-c-text`, `--sym-c-ring` | C background, text, outline | `#ffffff`, `#0e7490`, `#0891b2` | `#ffffff`, `#155e75`, `#0e7490` |
| `--sym-s`, `--sym-s-text`, `--sym-s-ring` | S background, text, outline | `#fff6d5`, `#334155`, `#475569` | `#fff6d5`, `#1e293b`, `#334155` |
| `--sym-x` | Background of other symbols | `#d4d4d4` | `#404040` |
| `--hit`, `--hit-glow` | Win outline and glow | `#10b981`, `rgba(16, 185, 129, .50)` | `#34d399`, same |
| `--mark` | Outline when a plugin adds the `mark` class | `#db2777` | Same |
| `--primary` | Accent color (buttons, selection) | `#059669` | `#047857` |

Other variables (backgrounds, borders, text colors and so on) are in `:root` of `app.css`.

### Injecting theme.css

1. Add `theme.css` to the plugin directory (the one `DevGames` points to, e.g. `internal/devgames/` below) with only the variables you change. A project that only wants different colors and has no plugins can keep just `theme.css` there.
2. The `embed.go` pattern must include css: `//go:embed *.js *.css`. With only `theme.css` in the directory, write `//go:embed theme.css`.
3. Restart the dev entry point. `theme` in `/dev/meta` becomes `true` and the panel loads `/dev/games/theme.css` after `app.css`.
4. While tuning colors, switch `devGames` to `os.DirFS("internal/devgames")` so a browser reload shows your changes without a restart.

Only `<gid>.js` and `theme.css` are served from `DevGames`; any other file name returns 404.

### Override scope

| Scope | Where | How |
| --- | --- | --- |
| The whole panel | `theme.css` | `:root { … }` |
| One game | `theme.css`, or that game's plugin `css` | `body[data-gid="<gid>"] { … }` |
| One symbol | Same as above | `.cell[data-sym="<symbol name>"] { … }`; for H and L change `--tone-base` (H's text color is `--tone-text`), for W, C and S change variables like `--sym-w` and `--sym-c-ring` |

Styles apply in the order `app.css` → `theme.css` → plugin `css`, later ones winning. Plugin styles stay on the page after switching games, so always scope plugin rules with `body[data-gid="<gid>"]`.

Dark defaults live in `:root:where([data-theme="dark"])`: the panel sets `data-theme="light"` or `"dark"` on `<html>` according to the theme button in the top bar (system, light, dark). When `theme.css` only writes `:root`, dark uses the same values; for different dark values add `:root[data-theme="dark"]` in `theme.css`. Do not use `@media (prefers-color-scheme: dark)`: it does not follow a manually switched theme.

```css
/* internal/devgames/theme.css */

/* The whole panel: the company's tone sequence and W colors */
:root {
  --tone-1: #0ea5e9;
  --tone-2: #f43f5e;
  --sym-w: #fef3c7; --sym-w-text: #92400e; --sym-w-ring: #d97706;
}
:root[data-theme="dark"] { --sym-w: #fde68a; }

/* Game 101 only */
body[data-gid="101"] { --sym-l-bg: 28%; /* a slightly deeper L background */ }

/* H1 and C1 of game 101 only */
body[data-gid="101"] .cell[data-sym="H1"] { --tone-base: #14b8a6; }
body[data-gid="101"] .cell[data-sym="C1"] { --sym-c-ring: #7c3aed; --sym-c-text: #6d28d9; }
```

## Using it in your own project

A project using problab needs three things: a lazily loaded Lab, plugins (optional) and an entry point. The example below uses a common layout: `pkg/engine` wires the engine, and `internal/...` holds configs, logic and plugins.

### 1. Add a dev constructor to the engine wiring

PRNG, configs, logic and optimal settings are the same as in production, but the Lab is assembled with `problab.New`, `RegisterAll` and `Freeze` instead of `NewAuto` (see `demo.NewProbLabForDev`):

```go
// pkg/engine/problab.go
func NewForDev() (*problab.Problab, error) {
	pb, err := problab.New(pRNGFactory, cfgs, logics, optimalOption)
	if err != nil {
		return nil, err
	}
	if err := pb.RegisterAll(); err != nil {
		_ = pb.Close()
		return nil, err
	}
	pb.Freeze()
	return pb, nil
}
```

- `NewAuto` preloads every game's optimal data at startup; assembled this way, each game loads it on its first Spin, so a dev session that looks at one or two games does not wait.
- Keep `NewAuto` for the production constructor: config and file errors show up at startup, and the first request does not wait for loading.

### 2. Write plugins (optional)

```text
internal/devgames/
  embed.go     // //go:embed *.js *.css  →  var FS embed.FS
  101.js       // DEV.register(101, {...})
  102.js
  theme.css    // optional: overrides the common colors, see "Custom colors" above
```

- See "Game plugins" above for how to write them; the plugins for demos 0–3 in `demo/demo_devgames/` work as templates.
- Games without a plugin still work and show the common panel only; add plugins later.
- `/dev/meta` marks each game's `plugin`, and the frontend loads plugins only for those games.

### 3. Add the entry point

Copy `cmd/dev/dev.go` into your own `cmd/dev/` and change only the two injection points at the top of the file; leave the flow below (creating and closing the Lab, handling Ctrl+C, calling the facade) unchanged:

```go
var (
	pLab     func() (*problab.Problab, error) = engine.NewForDev
	devGames fs.FS                            = devgames.FS
)
```

Or skip the copy and call the facade `server/api/dev/cli` from your own `main`. The contract matches `playerexp/cli` and `optimizer/v2/cli`: the caller creates and closes the Lab, and the entry point handles signals:

```go
code, err := devcli.Run(ctx, devcli.Options{
	Lab:         lab,       // required; closed by the caller
	DevGames:    devGames,  // nil shows the common panel only
	Addr:        ":5808",   // empty selects devcli.DefaultAddr
	OpenBrowser: true,
	Output:      os.Stdout, // nil prints no URL
	Lang:        "zh",      // default UI language: empty or "en" for English, "zh" for Chinese
})
```

The facade starts in `svrcfg.ModePanel`: only the landing page and `/dev`, no machine pools. `ModeDev` builds a machine pool per game for `/v1/spin` and loads all optimal data at startup, so do not use `ModeDev` for a dev entry point.

A Makefile target:

```make
dev:
	@go run ./cmd/dev
```

### 4. Run

```sh
make dev                                   # opens http://localhost:5808/dev
go run ./cmd/dev -addr :5810 -open=false   # another port, no browser
```

- Run from the project root: a relative optimal directory (e.g. `internal/optimal`) is only found from there.
- This entry point has only `/dev`, no `/v1/*`; start the production server separately when you need `/v1`.
- While writing plugins, temporarily switch `devGames` to `os.DirFS("internal/devgames")` so a browser reload picks up js changes without a restart; an `embed.FS` needs a restart.
- There is no authentication; do not expose it publicly.

### Upgrading from older versions

| Old usage | After upgrading |
| --- | --- |
| Calling `NewDevSimulator`, `DevSpinReport`, `DevSimReport` | Removed; compilation fails. Use `NewMachine` with `Machine.Spin` (one cycle at a time) for reproducible runs and `NewSimulator` for statistics |
| Calling `demo.NewServerConfig` | Removed; compilation fails. Assemble as described above |
| Your own dev entry point starting `server.Run` with `ModeDev` | Still works and the panel appears, but it builds machine pools for `/v1` and loads all optimal data at startup, without plugins. Rewriting it as above is recommended |
| Calling the old `/dev/spin`, `/dev/sim` | `/dev/spin` has a new format (see API above) and `/dev/sim` is removed; use `/v1/sim` or the Simulator for statistics |

## Font

The UI uses JetBrains Mono (SIL Open Font License 1.1), or the locally installed copy when available. See `NOTICE` and `third_party/licenses/JetBrainsMono_OFL-1.1.txt` for the license.
