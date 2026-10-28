// Skill 注册表的读写逻辑（config/config.json 的 skills 数组）。
//
// 为什么单独抽出来：`server.cjs` 有 4400 行、import 就会起服务，
// 里面这套 skill 读写逻辑没法写单测 —— 而它恰恰是最该被测的部分，
// 因为它直接覆盖 config.json，错了就是静默丢用户数据。
//
// 语义约定（和 agent 侧 skillroots.go 保持一致）：
//   - config.skills 是**唯一真相源**，条目集合只由它决定
//   - 目录扫描只用于补 description/tags/triggers 这类描述性字段
//   - 写入一律做**字段级合并**，绝不整条替换
//     （第一版 addSkillSetting 用 `skills[index] = entry` 整条覆盖，
//       把 description / tags / triggers / priority 全抹成了空）

const fs = require("node:fs");
const path = require("node:path");

const UNCATEGORIZED = "未分类";

function readJsonSafe(file) {
  try {
    const raw = fs.readFileSync(file, "utf8").replace(/^﻿/, "");
    return JSON.parse(raw || "{}");
  } catch {
    return {};
  }
}

function parseSkillDescription(skillFile) {
  try {
    const text = fs.readFileSync(skillFile, "utf8");
    const fm = text.match(/^---\r?\n([\s\S]*?)\r?\n---/);
    if (fm) {
      const m = fm[1].match(/description:\s*(.+)/);
      if (m) return m[1].trim().replace(/^["']|["']$/g, "");
    }
    return "";
  } catch {
    return "";
  }
}

/**
 * 读出全量注册表。
 * 返回的每条都带 category / core，前端直接按这两个字段分组渲染。
 */
function readSkillSettings({ appConfig, repoRoot, skillRoots }) {
  const cfg = readJsonSafe(appConfig);
  const configured = new Map();

  for (const skill of Array.isArray(cfg.skills) ? cfg.skills : []) {
    if (!skill || !skill.name) continue;
    configured.set(skill.name, {
      name: skill.name,
      description: skill.description || "",
      location: skill.location || "/skills/" + skill.name,
      enabled: skill.enabled !== false,
      core: skill.core === true,
      category: skill.category || UNCATEGORIZED,
      registered: true,
    });
  }

  for (const root of skillRoots) {
    let entries = [];
    try {
      entries = fs.readdirSync(root, { withFileTypes: true });
    } catch {
      continue;
    }
    for (const entry of entries) {
      if (!entry.isDirectory()) continue;
      const name = entry.name;
      const skillFile = path.join(root, name, "SKILL.md");
      if (!fs.existsSync(skillFile)) continue;

      const prev = configured.get(name) || {};
      const location =
        prev.registered && prev.location
          ? prev.location
          : root.startsWith(repoRoot)
            ? "/skills/" + name
            : homeRelative(root) + "/" + name;

      configured.set(name, {
        name,
        // 已登记的沿用 config 里的描述；没登记的才从 SKILL.md 解析。
        description: prev.description || parseSkillDescription(skillFile),
        location,
        // enabled / core / category 以 config 为准 —— 目录扫描不能覆盖用户设定。
        enabled: prev.enabled !== false,
        core: prev.core === true,
        category: prev.category || UNCATEGORIZED,
        registered: !!prev.registered,
      });
    }
  }

  return [...configured.values()].sort((a, b) =>
    a.name.localeCompare(b.name),
  );
}

/**
 * 按 category 分组，供两层树的第一层渲染。
 * 未分类的 skill 归到 UNCATEGORIZED 桶里，绝不丢弃 ——
 * 丢了就等于「刚加的 skill 看不见」。
 */
function groupSkillsByCategory(skills) {
  const buckets = new Map();
  for (const skill of skills) {
    const cat = skill.category || UNCATEGORIZED;
    if (!buckets.has(cat)) buckets.set(cat, []);
    buckets.get(cat).push(skill);
  }
  return [...buckets.entries()]
    .map(([category, items]) => ({
      category,
      skills: items,
      total: items.length,
      // 核心排前面，同为核心时按名称稳定排序
      sorted: [...items].sort((a, b) => {
        if (a.core !== b.core) return a.core ? -1 : 1;
        return a.name.localeCompare(b.name);
      }),
    }))
    .sort((a, b) => {
      // 「未分类」永远沉底，其余按名称
      if (a.category === UNCATEGORIZED) return 1;
      if (b.category === UNCATEGORIZED) return -1;
      return a.category.localeCompare(b.category);
    });
}

/**
 * 字段级合并写入 config.skills。
 *
 * patch 里出现过的字段才写；`null` 表示显式清空（如把分类改回未分类）。
 * 不在 patch 里的字段一律保留原值。
 */
function patchSkillSetting(patch, { appConfig }) {
  const key = String(patch.name || "").trim();
  if (!key) throw new Error("skill name is required");

  const cfg = readJsonSafe(appConfig);
  const skills = Array.isArray(cfg.skills) ? [...cfg.skills] : [];
  const index = skills.findIndex((s) => s && s.name === key);
  const has = (v) => Object.prototype.hasOwnProperty.call(patch, v) && patch[v] !== undefined;

  if (index < 0) {
    // 目录里有、config 没登记的条目也允许 patch：
    // 用户在前端点「归类」是合法操作，不该要求先手工编辑 config。
    // 补登时只写用户真正设置的字段，其余留空由 readSkillSettings 从磁盘补描述。
    if (!has("registered") && patch.registered !== true) {
      throw new Error("unknown skill: " + key);
    }
    const created = { name: key };
    if (has("enabled")) created.enabled = patch.enabled !== false;
    if (has("core")) created.core = patch.core === true;
    if (has("category")) {
      created.category = patch.category ? String(patch.category) : UNCATEGORIZED;
    }
    skills.push(created);
    cfg.skills = skills;
    const tmpNew = appConfig + ".tmp";
    fs.writeFileSync(tmpNew, JSON.stringify(cfg, null, 2));
    fs.renameSync(tmpNew, appConfig);
    return created;
  }

  // 合并，不是替换 —— 替换会把 description/tags/triggers/priority 全抹掉
  const merged = { ...skills[index] };
  if (has("enabled")) merged.enabled = patch.enabled !== false;
  if (has("core")) merged.core = patch.core === true;
  if (has("category")) {
    merged.category = patch.category ? String(patch.category) : UNCATEGORIZED;
  }
  if (has("description")) merged.description = String(patch.description);
  if (has("priority")) merged.priority = Number(patch.priority) || 0;
  skills[index] = merged;

  cfg.skills = skills;
  const tmp = appConfig + ".tmp";
  fs.writeFileSync(tmp, JSON.stringify(cfg, null, 2));
  fs.renameSync(tmp, appConfig);
  return merged;
}

/**
 * 移除一个 skill 的注册项，并按需删除它所在的目录。
 *
 * 这是删除类操作，所以刻意做得很保守：
 *   - name 只允许字母数字 _ . -，并且显式拒绝 "." / ".."。
 *     光靠字符白名单不够：'.' 是合法字符，所以正则会放过 ".."，而
 *     path.join(root, "..") 正好是 root 的父目录。下面那层「必须是 root 的
 *     直接子目录」检查最终也会挡住它，但那是巧合式的兜底，不是意图——
 *     删除路径上的东西不该靠巧合。
 *   - 目录只允许是 skillRoots 下**紧邻一层**的子目录，
 *     解析后的绝对路径必须还在某个 root 之内（防 symlink 逃逸）
 *   - 默认连目录一起删（deleteFiles !== false）。只删注册项不删目录的话，
 *     下一次 readSkillSettings 的目录扫描会把它重新捞回来，
 *     用户看到的就是「删了又冒出来」——所以默认必须删目录。
 */
function removeSkillSetting(name, { appConfig, skillRoots, deleteFiles = true }) {
  const key = String(name || "").trim();
  if (!key) throw new Error("skill name is required");
  if (!/^[A-Za-z0-9_.-]{1,64}$/.test(key) || key === "." || key === "..") {
    throw new Error("invalid skill name: " + key);
  }

  const roots = Array.isArray(skillRoots) ? skillRoots : [];
  let targetDir = null;
  for (const root of roots) {
    const candidate = path.join(root, key);
    // 必须是 root 的直接子目录，且 resolve 后仍在 root 之内
    const resolved = path.resolve(candidate);
    const resolvedRoot = path.resolve(root);
    if (path.dirname(resolved) !== resolvedRoot) continue;
    if (!resolved.startsWith(resolvedRoot + path.sep)) continue;
    if (fs.existsSync(candidate)) {
      targetDir = candidate;
      break;
    }
  }

  const cfg = readJsonSafe(appConfig);
  const skills = Array.isArray(cfg.skills) ? [...cfg.skills] : [];
  const before = skills.length;
  const index = skills.findIndex((s) => s && s.name === key);
  if (index >= 0) skills.splice(index, 1);

  // 既没有注册项、磁盘上也没有目录 → 早就没了，当成功处理（幂等）。
  if (index < 0 && !targetDir) {
    return { name: key, removed: false, removedFiles: false, reason: "not found" };
  }

  let removedFiles = false;
  if (targetDir && deleteFiles) {
    fs.rmSync(targetDir, { recursive: true, force: true });
    removedFiles = true;
  }

  if (index >= 0) {
    cfg.skills = skills;
    const tmp = appConfig + ".tmp";
    fs.writeFileSync(tmp, JSON.stringify(cfg, null, 2));
    fs.renameSync(tmp, appConfig);
  }

  return {
    name: key,
    removed: index >= 0,
    removedFiles,
    // 只摘了注册项、目录还在 —— 调用方要提醒用户下次扫描它会回来
    dirRemaining: !!targetDir && !removedFiles,
    remaining: before - skills.length,
  };
}

function homeRelative(p) {
  const home = process.env.HOME || "";
  return p.startsWith(home) ? "~" + p.slice(home.length) : p;
}

module.exports = {
  UNCATEGORIZED,
  readSkillSettings,
  groupSkillsByCategory,
  patchSkillSetting,
  removeSkillSetting,
};
