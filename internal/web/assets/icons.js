"use strict";

// Icons for the panel: 24px grid, 1.75 stroke, round ends. Each entry is a
// list of SVG elements as [tag, attributes].

const ICONS = {
  overview: [["rect", { x: 3.5, y: 3.5, width: 7, height: 7, rx: 1.5 }], ["rect", { x: 13.5, y: 3.5, width: 7, height: 7, rx: 1.5 }], ["rect", { x: 3.5, y: 13.5, width: 7, height: 7, rx: 1.5 }], ["rect", { x: 13.5, y: 13.5, width: 7, height: 7, rx: 1.5 }]],
  processes: [["circle", { cx: 6, cy: 7, r: 1.6 }], ["path", { d: "M10.5 7h9" }], ["circle", { cx: 6, cy: 12, r: 1.6 }], ["path", { d: "M10.5 12h9" }], ["circle", { cx: 6, cy: 17, r: 1.6 }], ["path", { d: "M10.5 17h6" }]],
  servers: [["rect", { x: 3.5, y: 4, width: 17, height: 6.5, rx: 1.75 }], ["rect", { x: 3.5, y: 13.5, width: 17, height: 6.5, rx: 1.75 }], ["path", { d: "M7 7.25h.01M7 16.75h.01M11 7.25h6M11 16.75h6" }]],
  settings: [["path", { d: "M4 7h9M17 7h3M4 17h3M11 17h9" }], ["circle", { cx: 15, cy: 7, r: 2 }], ["circle", { cx: 9, cy: 17, r: 2 }]],
  restart: [["path", { d: "M19.5 12a7.5 7.5 0 1 1-2.2-5.3" }], ["path", { d: "M19.5 4.5v4h-4" }]],
  stop: [["rect", { x: 6, y: 6, width: 12, height: 12, rx: 2 }]],
  play: [["path", { d: "M8 5.5v13l10.5-6.5z" }]],
  trash: [["path", { d: "M4.5 7h15M9.5 7V5h5v2M6.5 7l.8 12h9.4l.8-12M10 10.5v5.5M14 10.5v5.5" }]],
  plus: [["path", { d: "M12 5v14M5 12h14" }]],
  search: [["circle", { cx: 11, cy: 11, r: 6 }], ["path", { d: "M15.5 15.5 20 20" }]],
  chevronDown: [["path", { d: "m7 10 5 5 5-5" }]],
  chevronRight: [["path", { d: "m10 7 5 5-5 5" }]],
  arrowLeft: [["path", { d: "M19 12H5M11 6l-6 6 6 6" }]],
  menu: [["path", { d: "M4 7h16M4 12h16M4 17h16" }]],
  close: [["path", { d: "M6 6l12 12M18 6 6 18" }]],
  logout: [["path", { d: "M14 4.5H7a1.5 1.5 0 0 0-1.5 1.5v12A1.5 1.5 0 0 0 7 19.5h7" }], ["path", { d: "M11 12h9M17 9l3 3-3 3" }]],
  cpu: [["rect", { x: 6.5, y: 6.5, width: 11, height: 11, rx: 1.5 }], ["rect", { x: 9.75, y: 9.75, width: 4.5, height: 4.5, rx: 0.5 }], ["path", { d: "M10 3.5v3M14 3.5v3M10 17.5v3M14 17.5v3M3.5 10h3M3.5 14h3M17.5 10h3M17.5 14h3" }]],
  memory: [["rect", { x: 3.5, y: 7, width: 17, height: 10, rx: 1.5 }], ["path", { d: "M7.5 10.5v3M11 10.5v3M14.5 10.5v3M7 17v2.5M12 17v2.5M17 17v2.5" }]],
  disk: [["ellipse", { cx: 12, cy: 6.5, rx: 7.5, ry: 2.5 }], ["path", { d: "M4.5 6.5v11c0 1.4 3.4 2.5 7.5 2.5s7.5-1.1 7.5-2.5v-11M4.5 12c0 1.4 3.4 2.5 7.5 2.5s7.5-1.1 7.5-2.5" }]],
  clock: [["circle", { cx: 12, cy: 12, r: 8 }], ["path", { d: "M12 7.5V12l3 2" }]],
  alert: [["path", { d: "M12 4 3 19.5h18z" }], ["path", { d: "M12 10v4.5M12 17h.01" }]],
  check: [["circle", { cx: 12, cy: 12, r: 8 }], ["path", { d: "m8.5 12.2 2.4 2.3 4.6-4.8" }]],
  pulse: [["path", { d: "M3 12h4l2-5 4 10 2-5h6" }]],
  terminal: [["rect", { x: 3.5, y: 4.5, width: 17, height: 15, rx: 2 }], ["path", { d: "m7.5 9.5 3 2.5-3 2.5M12.5 15h4" }]],
  events: [["path", { d: "M7 4.5v15" }], ["circle", { cx: 7, cy: 7.5, r: 1.75 }], ["circle", { cx: 7, cy: 16.5, r: 1.75 }], ["path", { d: "M11 7.5h8.5M11 16.5h6" }]],
  config: [["path", { d: "M7 3.5h7l4.5 4.5v12.5h-11.5z" }], ["path", { d: "M14 3.5V8h4.5M9.5 12.5h5M9.5 16h5" }]],
  download: [["path", { d: "M12 4v11M7.5 10.5 12 15l4.5-4.5M5 19.5h14" }]],
  pause: [["path", { d: "M8.5 6v12M15.5 6v12" }]],
  copy: [["rect", { x: 8.5, y: 8.5, width: 11, height: 11, rx: 1.75 }], ["path", { d: "M15.5 8.5V6a1.5 1.5 0 0 0-1.5-1.5H6A1.5 1.5 0 0 0 4.5 6v8A1.5 1.5 0 0 0 6 15.5h2.5" }]],
  more: [["path", { d: "M6 12h.01M12 12h.01M18 12h.01", "stroke-width": 2.75 }]],
  sun: [["circle", { cx: 12, cy: 12, r: 3.75 }], ["path", { d: "M12 3v2M12 19v2M3 12h2M19 12h2M5.6 5.6 7 7M17 17l1.4 1.4M5.6 18.4 7 17M17 7l1.4-1.4" }]],
  moon: [["path", { d: "M19.5 14.5A7.5 7.5 0 0 1 9.5 4.5a7.5 7.5 0 1 0 10 10z" }]],
  monitor: [["rect", { x: 3.5, y: 4.5, width: 17, height: 11.5, rx: 1.75 }], ["path", { d: "M9 20h6M12 16v4" }]],
  save: [["path", { d: "M5.5 4.5h10l3 3v11a1 1 0 0 1-1 1h-12a1 1 0 0 1-1-1v-13a1 1 0 0 1 1-1z" }], ["path", { d: "M8.5 4.5v4h6v-4M8 19.5v-5h8v5" }]],
  link: [["path", { d: "M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1" }], ["path", { d: "M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1" }]],
  tunnel: [["path", { d: "M4 19.5V11a8 8 0 0 1 16 0v8.5" }], ["path", { d: "M8 19.5V11a4 4 0 0 1 8 0v8.5M3 19.5h18" }]],
  key: [["circle", { cx: 8, cy: 15, r: 3.5 }], ["path", { d: "m10.5 12.5 8-8M15.5 7.5l2.5 2.5M13.5 9.5l2 2" }]],
  bell: [["path", { d: "M6.5 16.5V11a5.5 5.5 0 0 1 11 0v5.5l1.5 2H5z" }], ["path", { d: "M10 20.5a2.2 2.2 0 0 0 4 0" }]],
  keyboard: [["rect", { x: 2.5, y: 6, width: 19, height: 12, rx: 2 }], ["path", { d: "M6 9.5h.01M9.5 9.5h.01M13 9.5h.01M16.5 9.5h.01M6 12.5h.01M18 12.5h.01M9.5 12.5h5M8 15h8" }]],
  info: [["circle", { cx: 12, cy: 12, r: 8 }], ["path", { d: "M12 11v5M12 8h.01" }]],
  wrap: [["path", { d: "M4 6h16M4 12h13a3 3 0 0 1 0 6h-4M4 18h5" }], ["path", { d: "m15 16-2 2 2 2" }]],
  flag: [["path", { d: "M5.5 20.5v-16M5.5 4.5h11l-2 4 2 4h-11" }]],
};

const SVG_NS = "http://www.w3.org/2000/svg";

function icon(name, size = 16) {
  const svg = document.createElementNS(SVG_NS, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("width", String(size));
  svg.setAttribute("height", String(size));
  svg.setAttribute("class", "icon");
  svg.setAttribute("aria-hidden", "true");
  for (const [tag, attrs] of ICONS[name] || []) {
    const e = document.createElementNS(SVG_NS, tag);
    for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, String(v));
    svg.append(e);
  }
  return svg;
}
