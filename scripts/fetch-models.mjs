/**
 * Download the models the runtime loads on demand.
 *
 * Run standalone (`node scripts/fetch-models.mjs`) or call ensureModels() from
 * the launcher. Everything present is skipped, so a normal start does no
 * network work at all.
 *
 * Two rules the rest of the project depends on:
 *   - a half-downloaded file never takes the final name, so an interrupted
 *     run leaves the previous state intact instead of a broken model
 *   - a failure is reported with what is missing and how to get it by hand,
 *     never swallowed
 */
import { createWriteStream, existsSync, mkdirSync, readdirSync, renameSync, rmSync, statSync } from "node:fs";
import { spawn, spawnSync } from "node:child_process";
import { Readable } from "node:stream";
import { pipeline } from "node:stream/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { MODELS, formatBytes } from "./model-manifest.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
export const repoRoot = path.resolve(here, "..");

const log = (message) => console.log(`[models] ${message}`);

export function modelDownloadUrls(rawUrl, env = process.env) {
  let parsed;
  try {
    parsed = new URL(rawUrl);
  } catch {
    return [rawUrl];
  }
  if (parsed.hostname !== "huggingface.co") return [rawUrl];
  parsed.hostname = "hf-mirror.com";
  const mirror = parsed.toString();
  return String(env.FAIRY_MODEL_OFFICIAL_FIRST || "") === "1"
    ? [rawUrl, mirror]
    : [mirror, rawUrl];
}

/** Human readable size of a downloaded file, refreshed while it grows. */
function progressPrinter(totalBytes, label) {
  let lastRender = 0;
  return (received) => {
    const now = Date.now();
    // Redrawing on every chunk spams the terminal on a fast link.
    if (now - lastRender < 400 && received !== totalBytes) return;
    lastRender = now;
    if (!totalBytes) return;
    const pct = ((received / totalBytes) * 100).toFixed(1);
    process.stdout.write(
      `\r[models] ${label} ${formatBytes(received)} / ${formatBytes(totalBytes)} (${pct}%)   `
    );
    if (received === totalBytes) process.stdout.write("\n");
  };
}

/**
 * Download one file to a temporary name, then move it into place.
 *
 * curl is used rather than fetch because Node's fetch ignores the
 * HTTP_PROXY / HTTPS_PROXY environment variables while curl honours them, and
 * a fair number of users sit behind a proxy. curl is also present on every
 * platform this project targets. fetch stays as a fallback for the case where
 * curl is missing.
 */
async function downloadFile(url, target, expectedBytes, label) {
  const temporary = `${target}.part`;
  mkdirSync(path.dirname(target), { recursive: true });

  const errors = [];
  for (const candidate of modelDownloadUrls(url)) {
    const viaCurl = await downloadWithCurl(candidate, temporary, expectedBytes, label);
    if (viaCurl.ok) return verifyAndFinish(temporary, target, expectedBytes, label);
    errors.push(`${candidate}: curl ${viaCurl.error}`);
    const viaFetch = await downloadWithFetch(candidate, temporary, expectedBytes, label);
    if (viaFetch.ok) return verifyAndFinish(temporary, target, expectedBytes, label);
    errors.push(`${candidate}: fetch ${viaFetch.error}`);
  }
  throw new Error(`${label} 下载失败：${errors.join("；")}`);
}

/** curl path. Returns {ok:true} or {ok:false,error}. */
function downloadWithCurl(url, temporary, expectedBytes, label) {
  return new Promise((resolve) => {
    const args = [
      "-fL", // fail on an HTTP error instead of writing the error page
      "--retry",
      "3",
      "--retry-delay",
      "2",
      "--connect-timeout",
      "20",
      "-A",
      "Fairy-model-fetcher/1.0",
      "-o",
      temporary,
    ];
    if (expectedBytes) {
      // Abort early when the server sends more than the manifest expects, so
      // a wrong URL cannot fill the disk.
      args.push("--max-filesize", String(expectedBytes + 1_048_576));
    }
    args.push(url);

    const child = spawn("curl", args, { stdio: ["ignore", "ignore", "pipe"] });
    let stderr = "";
    child.stderr.on("data", (chunk) => {
      stderr += chunk.toString();
    });
    child.on("error", (error) => {
      resolve({ ok: false, error: error.code === "ENOENT" ? "curl 未安装" : error.message });
    });
    child.on("close", (code) => {
      if (code === 0) return resolve({ ok: true });
      resolve({
        ok: false,
        error: `curl 退出码 ${code}${stderr ? `（${stderr.trim().slice(0, 160)}）` : ""}`,
      });
    });
  });
}

/** fetch fallback for systems without curl. */
async function downloadWithFetch(url, temporary, expectedBytes, label) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 15 * 60 * 1000);
  try {
    const response = await fetch(url, {
      redirect: "follow",
      signal: controller.signal,
      headers: { "User-Agent": "Fairy-model-fetcher/1.0" },
    });
    if (!response.ok) return { ok: false, error: `HTTP ${response.status}` };
    const total = Number(response.headers.get("content-length")) || expectedBytes || 0;
    const onProgress = progressPrinter(total, path.basename(temporary));
    const source = Readable.fromWeb(response.body);
    source.on("data", (chunk) => onProgress(chunk.length));
    await pipeline(source, createWriteStream(temporary));
    clearTimeout(timer);
    return { ok: true };
  } catch (error) {
    clearTimeout(timer);
    // Never leave a partial file where a real one is expected.
    try {
      rmSync(temporary, { force: true });
    } catch {}
    return {
      ok: false,
      error: error?.name === "AbortError" ? "下载超时" : error?.message || String(error),
    };
  }
}

/**
 * Whether a size on disk is acceptable for an expected size.
 *
 * Weight files must match exactly: a different size means the wrong file.
 * Small files get 5% tolerance because the upstream edits its documentation.
 */
function sizeMatches(actual, expected) {
  if (!expected) return true;
  const tolerance = expected > 1_048_576 ? 0 : expected * 0.05;
  return Math.abs(actual - expected) <= tolerance;
}

/** Size check plus the atomic rename into the final path. */
function verifyAndFinish(temporary, target, expectedBytes, label) {
  const actual = statSync(temporary).size;
  if (!sizeMatches(actual, expectedBytes)) {
    rmSync(temporary, { force: true });
    throw new Error(
      `${label} 体积不符：期望 ${formatBytes(expectedBytes)}，实得 ${formatBytes(actual)}`
    );
  }
  renameSync(temporary, target);
  return actual;
}

/** Run tar to unpack an archive. Returns the top level directory it created. */
function extractArchive(archivePath, destination, expectedTopLevel) {
  mkdirSync(destination, { recursive: true });
  const result = spawnSync("tar", ["-xjf", archivePath, "-C", destination], {
    stdio: ["ignore", "ignore", "pipe"],
  });
  if (result.status !== 0) {
    throw new Error(`解包失败：${(result.stderr || "").toString().trim().slice(0, 200)}`);
  }
  return expectedTopLevel;
}

/** True when the model directory already has what the runtime needs. */
function isModelPresent(model) {
  const dir = path.join(repoRoot, model.dir);
  if (!existsSync(dir)) return false;
  // Every model here has an explicit file list, including the ones that
  // arrive as a tarball, so one check covers both shapes. A tarball that
  // unpacked without its weights fails this and gets downloaded again.
  if (model.files?.length) {
    return model.files.every((file) => existsSync(path.join(dir, file.name)));
  }
  return true;
}

/** Fetch one model. Returns true when something was downloaded. */
async function fetchModel(model, options = {}) {
  const dir = path.join(repoRoot, model.dir);
  const force = options.force || process.env.FORCE_MODELS === "1";

  if (!force && isModelPresent(model)) {
    log(`${model.title} 已存在，跳过（${model.source}）`);
    return false;
  }

  log(`${model.title} 开始下载（${model.source}，约 ${formatBytes(model.approxBytes)}）`);

  if (model.archive) {
    const archive = path.join(dir, "..", path.basename(model.archive.url));
    await downloadFile(model.archive.url, archive, 0, path.basename(archive));
    try {
      const topLevel = extractArchive(archive, dir, model.archive.topLevel);
      const extracted = path.join(dir, topLevel);
      if (existsSync(extracted)) {
        // The tarball has its own folder; flatten it so the runtime finds the
        // weights where it expects them.
        for (const entry of readdirSync(extracted)) {
          const from = path.join(extracted, entry);
          const to = path.join(dir, entry);
          if (!existsSync(to)) renameSync(from, to);
        }
        rmSync(extracted, { recursive: true, force: true });
      }
    } finally {
      rmSync(archive, { force: true });
    }
    log(`${model.title} 完成`);
    return true;
  }

  const repo = (model.repos || [])[0];
  for (const file of model.files || []) {
    const target = path.join(dir, file.name);
    // An existing file counts as present when its size is within the same
    // tolerance verifyAndFinish applies, so the two never disagree.
    if (!force && existsSync(target) && sizeMatches(statSync(target).size, file.bytes)) continue;
    await downloadFile(model.downloadUrl(repo, file.name), target, file.bytes, file.name);
  }
  log(`${model.title} 完成`);
  return true;
}

/**
 * Make sure the models exist, downloading only what is missing.
 *
 * @param {object} options
 * @param {boolean} options.force download even when present
 * @param {string[]} options.only  restrict to these model ids
 * @returns {Promise<{downloaded: string[], failed: Array<{id:string,error:string}>}>}
 */
export async function ensureModels(options = {}) {
  const selected = options.only?.length
    ? MODELS.filter((model) => options.only.includes(model.id))
    : MODELS;

  const downloaded = [];
  const failed = [];

  for (const model of selected) {
    try {
      if (await fetchModel(model, options)) downloaded.push(model.id);
    } catch (error) {
      failed.push({ id: model.id, error: error?.message || String(error) });
      log(`${model.title} 未完成：${error?.message || error}`);
    }
  }

  if (downloaded.length) {
    log(`已下载 ${downloaded.length} 个模型`);
  }

  if (failed.length) {
    log("以下模型缺失，对应功能会不可用（不影响启动）：");
    for (const item of failed) {
      const model = MODELS.find((m) => m.id === item.id);
      log(`  · ${model?.title || item.id} —— ${item.error}`);
      log(`    手动获取：${model?.homepage || "见 scripts/model-manifest.mjs"}`);
    }
  }

  return { downloaded, failed };
}

export { isModelPresent };

// Direct invocation: `node scripts/fetch-models.mjs [--force] [id...]`
if (process.argv[1] && path.resolve(process.argv[1]) === path.resolve(fileURLToPath(import.meta.url))) {
  const args = process.argv.slice(2);
  const force = args.includes("--force");
  const only = args.filter((arg) => !arg.startsWith("--"));
  ensureModels({ force, only }).then((result) => {
    process.exit(result.failed.length ? 1 : 0);
  });
}
