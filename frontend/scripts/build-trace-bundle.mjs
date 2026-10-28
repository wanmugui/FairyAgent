/**
 * Rebuild src/utils/trace-view-bundle.js.
 *
 * The bundle is the Trace view compiled into one self-contained script, so
 * the exported HTML works without the app and without a network. It used to
 * be built with esbuild; esbuild is not installed in this checkout, so this
 * script drives the same job through vite's bundler instead.
 *
 * The contract the bundle has to keep:
 *   - it defines a global named TraceExport
 *   - TraceExport.renderTrace(rootEl, data, name) renders the trace view
 */
import { build } from "vite";
import { writeFileSync, mkdirSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, "..");
const outFile = resolve(repoRoot, "src/utils/trace-view-bundle.js");

const result = await build({
  root: repoRoot,
  logLevel: "warn",
  define: { "process.env.NODE_ENV": '"production"' },
  build: {
    write: false,
    minify: "esbuild",
    lib: {
      entry: resolve(repoRoot, "src/utils/export-trace-entry.jsx"),
      formats: ["iife"],
      name: "TraceExport",
      fileName: () => "trace-view-bundle.js",
    },
    rollupOptions: {
      output: { extend: true },
    },
  },
});

// vite 的 build() 返回数组，每项里有 RollupOutput，产物在 output 数组里。
const rolls = Array.isArray(result) ? result : [result];
const chunks = rolls.flatMap((r) => (Array.isArray(r?.output) ? r.output : []));
const output = chunks.find((chunk) => chunk.type === "chunk");
if (!output || !output.code) {
  console.error("构建没有产出 JS");
  process.exit(1);
}

mkdirSync(dirname(outFile), { recursive: true });
writeFileSync(outFile, output.code, "utf8");
console.log(`已写入 ${outFile}（${output.code.length} 字节）`);
