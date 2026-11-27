# The web panel's design

What the panel looks like and why. Read this before changing `internal/web/assets/`.

## What it is for

A person opens the panel to answer one question first: **is anything wrong?** Then, if so: which process, since when, and what do I do. Everything else (starting an app, reading its logs, adding a server) comes second. The panel is a tool someone sits in for minutes, not a page to be impressed by, so it is built to be dense, quiet and fast to read.

## What the research said

| Source | What we took from it |
|---|---|
| [Linear: behind the latest design refresh](https://linear.app/now/behind-the-latest-design-refresh) | Keep the density, lower the noise. "Don't compete for attention you haven't earned": what the person is working on stays in focus, navigation recedes (a dimmer sidebar). "Structure should be felt, not seen": fewer separators, softer contrast, fewer and smaller icons, no colored icon backgrounds. |
| Dashboard guides (Grafana's best practices, the dashboard-design write-ups from VMware, ITRS and others) | Someone should know within five seconds whether something is wrong, where, and how bad. Start with a summary of state, then drill down. Use status indicators and roll-ups rather than walls of charts. Never show status by color alone: a word or shape goes with it. |
| Data-table guides ([data density patterns](https://artofstyleframe.com/blog/dashboard-data-density-patterns/), the Elastic and Adobe table guidelines) | Rows of 36 to 40 px with 13 to 14 px type read well and show 8 to 10 rows more per screen than 50 px rows. Left-align text, right-align numbers, use tabular figures. Keep to six to eight columns and one line per row. Sticky headers must be opaque. Below tablet width go back to roomy rows (44 px touch targets). |
| "AI slop" catalogs ([the ban list](https://github.com/Laith0003/ux-skill/wiki/Anti-AI-slop-ban-list) and similar) | The tells: a row of identical "big number, small label, icon" cards; icons in tinted squares; cards inside cards; every group wrapped in a card; indigo/violet accents, gradients, glow and glass; small uppercase letter-spaced labels everywhere; Title Case everywhere; emoji and mixed icon sets; pulsing and fade-in motion with no meaning; round invented numbers; filler copy. |

## Decisions

1. **Answer first.** The overview opens with one sentence of state ("All 7 processes are running", "1 of 7 needs attention") and the few numbers that matter beside it. What needs attention comes before anything else. The big-number card row is gone.
2. **Full width.** No page has a maximum width. Content uses the whole window; on wide screens the columns get wider and a third column appears, instead of the content sitting in a left-hand strip with empty space beside it.
3. **Content in focus, chrome recedes.** The sidebar is a notch dimmer than the page, its selected item is a quiet background and not a bordered pill, and its icons are small. Lines between rows are faint hairlines. Borders are kept for tables, inputs and menus, and nothing sits inside a bordered thing that is itself bordered.
4. **Dense, calm tables.** One line per row at 38 px. Name and command share the line. Numbers are right-aligned with tabular figures. A column that is empty for every row (health, when nothing has a health check) is not drawn. Row actions appear on hover and keyboard focus, and always on touch screens. A footer line sums the table.
5. **Color is state.** Ink and paper for everything, one green that means running, amber for restarting and unsupervised, red for failed. Every status is a shape and a word as well as a color. No gradients, no glow, no second accent.
6. **Type.** Inter for the interface, JetBrains Mono for things that are code (commands, paths, pids, logs). Numbers in tables and stats are set in the interface face with tabular figures, not in mono. Labels are sentence case, 12 px, muted, medium weight; there is no uppercase letter-spaced text. Weight carries hierarchy: 600 for titles and names, 500 for controls, 400 for the rest.
7. **Shapes vary by role.** Controls 6 px, panels 8 px, chips and bars fully round. No shadows except on menus and dialogs.
8. **Every state is designed.** Hover, focus (a visible ring), pressed, disabled, loading (skeleton rows with the shape of the content, not the word "Loading"), empty, and error each have a look.
9. **Motion shows state.** 120 ms on hover and focus. The only continuous animation is the spinner of a process that is restarting, and the live dot while logs stream. Everything respects `prefers-reduced-motion`.
10. **Voice.** Sentence case, concrete: "gave up after 15 restarts", not "Something went wrong".

## Layout

- **Shell**: a 220 px sidebar and the page. Below 900 px the sidebar becomes a drawer behind a menu button.
- **Page**: padding of 20 px by 28 px (32 px from 1800 px up), a header with the title, one muted line and the actions, then the content.
- **Overview**: state band, then two columns: what needs attention, the servers and the busiest processes on the left, activity on the right. From 1800 px the columns widen and the gutter grows.
- **Processes**: toolbar (filter, state filter, count), table, summary line.
- **A process**: header (name, state, meta, actions), plain text tabs, then on the overview tab two wide charts side by side, below them the details and the recent events. Logs fill the rest of the window.
- **Below 720 px** a process row becomes two lines (name and command, then state and numbers) with a 52 px height.

## Tokens

Defined at the top of `style.css`. Light uses Paper (`#f7f7f6`) as the page and a dimmer `#efefed` sidebar; dark uses `#131519` for the page and `#0d0e11` for the sidebar. Text, muted and faint steps keep at least 4.5:1 against their background for anything that carries meaning; faint is for decoration and for numbers that are not needed to read the row.

## Checking a change

Look at every page at 1440, 1920 and 390 px wide, in light and dark, with an empty list, one process, a group, a crashing process and a server that is down. Tab through the page: every control shows a focus ring. `internal/web/href_guard_test.go` keeps every link inside the panel.

## Update for v0.5.0: a livelier dashboard

The first redesign was deliberately plain. Feedback was that the panel should feel like a dashboard people enjoy, so v0.5.0 relaxes decisions 1, 8 and 9 and leaves the rest:

- **The overview** opens with a greeting and the one-sentence answer, then four KPI cards (processes, CPU, memory, restarts). Each carries a status chip with a word, so state is never color alone. Below sits a resource chart (CPU or memory of everything, last two minutes) with a rule and tooltip that follow the pointer. The "identical big-number cards" tell is accepted here because every card is also a state indicator, and the table and attention list remain the main content.
- **Sign-in** is a split card: an illustration of our own (solid fills in our ink and green, no gradients) beside a large form.
- **Motion** is allowed where it explains: pages and cards rise in, LEDs in the illustrations blink, skeleton blocks pulse, empty states have a small picture. All of it stops under `prefers-reduced-motion`.
- **Every state** has a skeleton (first load) and an empty state with a picture and a sentence.
- **Settings** gains a log terminal section (ten color themes in the style of Termius/iTerm, text size, wrap, a live preview), password change and mode switching. Long lines wrap by default.
- **Every change asks first**: restart, stop, delete, mode switch, password change, saving the list. The TUI asks in its footer (`y` confirms, anything else cancels).

- **No decorative icons** (v0.5.3 feedback): tinted icon squares and circles on cards, headings and activity read as generated, so they are out. Cards carry a word chip for state; activity uses a small state dot.
