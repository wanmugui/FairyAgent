const fs = require("fs");
const path = require("path");

function readStripped(filePath) {
  try { return fs.readFileSync(filePath, "utf-8").replace(/^\uFEFF/, ""); }
  catch { return null; }
}

function unescapeXmlText(value) {
  return String(value || "")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&amp;/g, "&");
}

function isDescendant(parent, child) {
  const relative = path.relative(parent, child);
  return relative !== "" && relative !== ".." && !relative.startsWith(".." + path.sep) && !path.isAbsolute(relative);
}

function resolveDeckPath(deckDir, workspaceRoot, virtualDeckRoot) {
  const normalizedDeckDir = String(deckDir || "").trim().replace(/\\/g, "/");
  const resolvedWorkspaceRoot = path.resolve(workspaceRoot);
  let deckPath;

  if (normalizedDeckDir.startsWith(virtualDeckRoot)) {
    const relative = path.posix.normalize(normalizedDeckDir.slice(virtualDeckRoot.length));
    if (!relative || relative === "." || relative === ".." || relative.startsWith("../")) return null;
    deckPath = path.resolve(resolvedWorkspaceRoot, ...relative.split("/"));
  } else if (path.isAbsolute(normalizedDeckDir)) {
    // The local agent rewrites the virtual deck_dir to this workspace path
    // before persisting the session. Accept it only after the same boundary
    // validation as the virtual form.
    deckPath = path.resolve(normalizedDeckDir);
  } else {
    return null;
  }

  if (!isDescendant(resolvedWorkspaceRoot, deckPath)) return null;

  // Resolve symlinks before returning a file path, so an in-workspace symlink
  // cannot make the preview endpoint read a file outside the deck workspace.
  try {
    const realWorkspaceRoot = fs.realpathSync(resolvedWorkspaceRoot);
    const realDeckPath = fs.realpathSync(deckPath);
    if (!isDescendant(realWorkspaceRoot, realDeckPath)) return null;

    const outlinePath = path.join(realDeckPath, "outline.md");
    const realOutlinePath = fs.realpathSync(outlinePath);
    if (!isDescendant(realDeckPath, realOutlinePath) || !fs.statSync(realOutlinePath).isFile()) return null;
    return realOutlinePath;
  } catch {
    return null;
  }
}

function findDeckDirInPptConfig(content) {
  if (typeof content !== "string") return "";
  const configs = [...content.matchAll(/<ppt_config\b[^>]*>([\s\S]*?)<\/ppt_config>/gi)];
  let deckDir = "";
  for (const config of configs) {
    const match = config[1].match(/<deck_dir>([\s\S]*?)<\/deck_dir>/i);
    if (match) deckDir = unescapeXmlText(match[1]);
  }
  return deckDir;
}

function findDeckDirInMessage(message) {
  if (message?.role === "user") return findDeckDirInPptConfig(message.content);

  // ask_user answers are persisted as role=tool so that they remain paired
  // with the assistant tool call. Current local sessions wrap the answer in
  // JSON, while production sessions may store the raw answer directly.
  if (message?.role !== "tool" || message?.name !== "ask_user") return "";
  const directDeckDir = findDeckDirInPptConfig(message.content);
  if (directDeckDir) return directDeckDir;

  try {
    const payload = JSON.parse(message.content);
    return findDeckDirInPptConfig(payload?.answer);
  } catch {
    return "";
  }
}

// PPT confirmations may only preview the outline belonging to the deck declared
// in this session's own <ppt_config>. Do not accept a path from the browser.
function resolveSessionPptOutline(name, { sessionsDir, workspaceRoot, virtualDeckRoot = "/mnt/data/result/" }) {
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(String(name || ""))) return null;
  const raw = readStripped(path.join(sessionsDir, name, name + ".json"));
  if (!raw) return null;

  let messages;
  try { messages = JSON.parse(raw).messages || []; } catch { return null; }
  let deckDir = "";
  for (const message of messages) {
    const candidate = findDeckDirInMessage(message);
    if (candidate) deckDir = candidate;
  }
  return resolveDeckPath(deckDir, workspaceRoot, virtualDeckRoot);
}

module.exports = { resolveSessionPptOutline };
