// Render the real component; no network, browser, service or test fixture writes.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import Module from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";

const filename = fileURLToPath(new URL("../src/components/ConsoleSourceTimes.tsx", import.meta.url));
const compiled = new Module(filename);
compiled.filename = filename;
compiled.paths = Module._nodeModulePaths(path.dirname(filename));
compiled._compile(ts.transpileModule(await readFile(filename, "utf8"), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 },
}).outputText, filename);
const Component = compiled.exports.default;
const render = value => renderToStaticMarkup(createElement(Component, { value }));

assert.equal(render(undefined), "", "ordinary and older records have no fabricated provenance");
assert.equal(render(null), "");
const sql = render({ sourceId: "41", createdAt: "2020-08-09 16:20:30", updatedAt: null });
assert.match(sql, /原系统创建时间/);
assert.match(sql, /2020-08-09 16:20:30/);
assert.match(sql, /源记录为空/);
assert.match(sql, /未作时区换算/);
assert.doesNotMatch(sql, /datetime=|<input|<textarea|<button/);
assert.doesNotMatch(sql, /2020-08-09T|16:20:30Z/);
assert.match(render({ sourceId: "42", updatedAt: "2020-08-09T16:20:30-05:00" }), /2020-08-09T16:20:30-05:00/);
assert.match(render({ sourceId: "42" }), /未提供源字段/);
assert.match(render({ sourceId: "43", createdAt: "0000-00-00 00:00:00" }), /0000-00-00 00:00:00（原系统零值，时间不可用）/);
const unsafe = render({ sourceId: "<script>bad()</script>", createdAt: "<img src=x onerror=bad()>" });
assert.doesNotMatch(unsafe, /<script>|<img/);
assert.match(unsafe, /&lt;script&gt;/);
console.log("PASS Console source-time rendering preserves raw wall-clock/offset/null/missing/zero semantics and escaped read-only output");
