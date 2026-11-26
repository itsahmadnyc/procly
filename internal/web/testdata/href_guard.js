// Runs the real h() and insidePanel() from assets/app.js against a minimal fake
// DOM. Usage: node href_guard.js ../assets/app.js (see TestPanelHrefGuard).
const fs = require("fs"), vm = require("vm");
const src = fs.readFileSync(process.argv[2], "utf8");
const start = src.indexOf("const URL_ATTRS");
const end = src.indexOf("// fill sets el's children");
if (start < 0 || end < 0) throw new Error("could not find h() in app.js");
class Node {}
class El extends Node {
  constructor(tag) { super(); this.tag = tag; this.attrs = {}; this.children = []; this.listeners = []; this.className = ""; this.textContent = ""; }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  addEventListener(n, f) { this.listeners.push(n); }
  append(...c) { this.children.push(...c); }
}
const ctx = { Node, document: { createElement: (t) => new El(t), createTextNode: (t) => ({ text: t }) } };
vm.createContext(ctx);
vm.runInContext(src.slice(start, end) + "\nthis.h = h; this.insidePanel = insidePanel;", ctx);
let bad = 0;
const check = (name, ok) => { if (!ok) bad++; console.log((ok ? "ok   " : "FAIL ") + name); };
const attr = (k, v) => ctx.h("a", { [k]: v }).attrs[k];
for (const v of ["#/", "#/s/local/p/api", "/assets/x.png", "blob:http://127.0.0.1:1/abc"]) check(`href ${v} kept`, attr("href", v) === v);
for (const v of ["javascript:alert(1)", "JavaScript:alert(1)", " javascript:alert(1)", "data:text/html,x", "https://evil.example/", "//evil.example/", "vbscript:x", "\tjavascript:x"]) check(`href ${JSON.stringify(v)} dropped`, attr("href", v) === undefined);
check("src javascript: dropped", attr("src", "javascript:x") === undefined);
check("title is untouched", ctx.h("a", { title: "javascript:x" }).attrs.title === "javascript:x");
check("class and text still work", ctx.h("a", { class: "c", text: "t" }).className === "c");
check("handlers still attach", ctx.h("a", { onclick: () => {} }).listeners[0] === "click");
process.exit(bad ? 1 : 0);
