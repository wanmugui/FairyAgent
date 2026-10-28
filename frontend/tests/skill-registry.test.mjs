// Skill 注册表读写的回归测试。
//
// 重点盯两件事：
//  1. 写入必须是**字段级合并**，不能整条替换
//     （真实事故：`skills[index] = entry` 把 description/tags/triggers/priority 全抹空，
//       而用户界面上完全看不出来——列表还在，只是内容没了）
//  2. 目录扫描不能覆盖用户在 config 里设定的 enabled / core / category

import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

import { createRequire } from "node:module";
const require_ = createRequire(import.meta.url);
const {
  readSkillSettings,
  groupSkillsByCategory,
  patchSkillSetting,
  removeSkillSetting,
  UNCATEGORIZED,
} = require_("../src/skillRegistry.cjs");

function mkFixture() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "skillreg-"));
  const repoRoot = path.join(root, "repo");
  const skillRoot = path.join(repoRoot, "skills");
  const appConfig = path.join(root, "config.json");
  fs.mkdirSync(skillRoot, { recursive: true });

  const writeSkill = (name, desc) => {
    fs.mkdirSync(path.join(skillRoot, name), { recursive: true });
    fs.writeFileSync(
      path.join(skillRoot, name, "SKILL.md"),
      `---\nname: ${name}\ndescription: ${desc}\n---\n\n正文\n`,
    );
  };
  writeSkill("alpha", "磁盘上的 alpha 描述");
  writeSkill("beta", "磁盘上的 beta 描述");
  writeSkill("gamma", "磁盘上的 gamma 描述");

  const cfg = {
    other_key: "must survive",
    skills: [
      {
        name: "alpha",
        description: "config 里的 alpha 描述",
        location: "/skills/alpha",
        tags: ["a", "tag"],
        triggers: ["触发A"],
        priority: 70,
        enabled: true,
        category: "3D",
        core: true,
      },
      { name: "beta", description: "beta", location: "/skills/beta" },
    ],
  };
  fs.writeFileSync(appConfig, JSON.stringify(cfg, null, 2));

  return { root, repoRoot, skillRoot, appConfig };
}

// 读取必须返回全部 skill，且带 category / core
test("readSkillSettings 返回全部条目并带 category/core", () => {
  const f = mkFixture();
  const skills = readSkillSettings({
    appConfig: f.appConfig,
    repoRoot: f.repoRoot,
    skillRoots: [f.skillRoot],
  });

  const byName = Object.fromEntries(skills.map((s) => [s.name, s]));
  assert.equal(skills.length, 3, "磁盘 3 个都应出现");
  assert.equal(byName.alpha.category, "3D");
  assert.equal(byName.alpha.core, true);
  assert.equal(byName.alpha.enabled, true);
  assert.equal(byName.beta.category, UNCATEGORIZED, "没分类的应落到未分类桶");
  assert.equal(byName.gamma.registered, false, "gamma 磁盘有但 config 没登记");
});

// 已登记的 description 以 config 为准
test("readSkillSettings 保留 config 的 description", () => {
  const f = mkFixture();
  const skills = readSkillSettings({
    appConfig: f.appConfig,
    repoRoot: f.repoRoot,
    skillRoots: [f.skillRoot],
  });
  const alpha = skills.find((s) => s.name === "alpha");
  assert.equal(alpha.description, "config 里的 alpha 描述");
});

// 未登记的才从 SKILL.md 解析描述
test("readSkillSettings 未登记的从 SKILL.md 补描述", () => {
  const f = mkFixture();
  const skills = readSkillSettings({
    appConfig: f.appConfig,
    repoRoot: f.repoRoot,
    skillRoots: [f.skillRoot],
  });
  const gamma = skills.find((s) => s.name === "gamma");
  assert.equal(gamma.description, "磁盘上的 gamma 描述");
});

test("目录扫描不得覆盖 config 里的 enabled=false", () => {
  const f = mkFixture();
  const cfg = JSON.parse(fs.readFileSync(f.appConfig, "utf8"));
  cfg.skills[0].enabled = false;
  fs.writeFileSync(f.appConfig, JSON.stringify(cfg, null, 2));

  const skills = readSkillSettings({
    appConfig: f.appConfig,
    repoRoot: f.repoRoot,
    skillRoots: [f.skillRoot],
  });
  assert.equal(skills.find((s) => s.name === "alpha").enabled, false);
});

// ★ 回归：patch 只改一个字段，其余字段必须原样保留
test("patchSkillSetting 切 category 不丢其他字段", () => {
  const f = mkFixture();
  patchSkillSetting(
    { name: "alpha", category: "逆向" },
    { appConfig: f.appConfig },
  );

  const cfg = JSON.parse(fs.readFileSync(f.appConfig, "utf8"));
  const alpha = cfg.skills.find((s) => s.name === "alpha");
  assert.equal(alpha.category, "逆向");
  assert.equal(alpha.description, "config 里的 alpha 描述", "description 不能丢");
  assert.deepEqual(alpha.tags, ["a", "tag"], "tags 不能丢");
  assert.deepEqual(alpha.triggers, ["触发A"], "triggers 不能丢");
  assert.equal(alpha.priority, 70, "priority 不能丢");
  assert.equal(alpha.core, true, "core 不能丢");
  assert.equal(cfg.other_key, "must survive", "其他顶层配置不能丢");
});

test("patchSkillSetting 切 core 不丢 category", () => {
  const f = mkFixture();
  patchSkillSetting({ name: "alpha", core: false }, { appConfig: f.appConfig });
  const cfg = JSON.parse(fs.readFileSync(f.appConfig, "utf8"));
  const alpha = cfg.skills.find((s) => s.name === "alpha");
  assert.equal(alpha.core, false);
  assert.equal(alpha.category, "3D");
  assert.deepEqual(alpha.tags, ["a", "tag"]);
});

test("patchSkillSetting 切 enabled 不丢其他字段", () => {
  const f = mkFixture();
  patchSkillSetting({ name: "alpha", enabled: false }, { appConfig: f.appConfig });
  const cfg = JSON.parse(fs.readFileSync(f.appConfig, "utf8"));
  const alpha = cfg.skills.find((s) => s.name === "alpha");
  assert.equal(alpha.enabled, false);
  assert.equal(alpha.category, "3D");
  assert.deepEqual(alpha.triggers, ["触发A"]);
});

test("patchSkillSetting 持久化后可被重新读出", () => {
  const f = mkFixture();
  patchSkillSetting({ name: "alpha", category: "视频" }, { appConfig: f.appConfig });
  const skills = readSkillSettings({
    appConfig: f.appConfig,
    repoRoot: f.repoRoot,
    skillRoots: [f.skillRoot],
  });
  assert.equal(skills.find((s) => s.name === "alpha").category, "视频");
});

test("patchSkillSetting 拒绝未登记的 skill", () => {
  const f = mkFixture();
  assert.throws(
    () => patchSkillSetting({ name: "gamma", category: "x" }, { appConfig: f.appConfig }),
    /unknown skill/,
    "未登记的 skill 不该被 patch 静默创建",
  );
});

test("patchSkillSetting 需要 name", () => {
  const f = mkFixture();
  assert.throws(() => patchSkillSetting({}, { appConfig: f.appConfig }));
});

// 分组：未分类沉底，核心排前
test("groupSkillsByCategory 未分类沉底且核心排前", () => {
  const groups = groupSkillsByCategory([
    { name: "a", category: "3D", core: false },
    { name: "b", category: "3D", core: true },
    { name: "c", category: "", core: false },
    { name: "d", category: "3D", core: true },
  ]);
  const names = groups.map((g) => g.category);
  assert.equal(names.at(-1), UNCATEGORIZED, "未分类必须沉底");

  const g3d = groups.find((g) => g.category === "3D");
  assert.equal(g3d.total, 3);
  assert.deepEqual(
    g3d.sorted.map((s) => s.name),
    ["b", "d", "a"],
    "核心应排在非核心前面",
  );
});

// 空分类不能丢 skill
test("groupSkillsByCategory 不丢条目", () => {
  const skills = [
    { name: "a", category: "", core: false },
    { name: "b", core: false },
  ];
  const groups = groupSkillsByCategory(skills);
  const total = groups.reduce((n, g) => n + g.total, 0);
  assert.equal(total, 2, "无分类的 skill 也不能消失");
});

// 真实 config.json 必须仍可被读出（防止改动破坏线上配置）
test("真实 config.json 可正常读取", () => {
  const repoRoot = path.resolve(import.meta.dirname, "..");
  const appConfig = path.join(repoRoot, "config", "config.json");
  if (!fs.existsSync(appConfig)) return;

  const skills = readSkillSettings({
    appConfig,
    repoRoot,
    skillRoots: [path.join(repoRoot, "skills")],
  });
  assert.ok(skills.length > 0, "真实 config 至少要读出一些 skill");
  for (const s of skills) {
    assert.ok(s.category, "每条都要有 category（含兜底）");
    assert.equal(typeof s.core, "boolean");
    assert.equal(typeof s.enabled, "boolean");
  }
});

// ---------------------------------------------------------------------------
// removeSkillSetting
//
// 删除的关键性质：注册项和目录必须一起消失。只摘注册项的话，下一次
// readSkillSettings 的目录扫描会立刻把它捞回来，用户看到的就是「删了又冒出来」。
// ---------------------------------------------------------------------------

test("删除会同时移除注册项和目录", () => {
  const fx = mkFixture();
  const result = removeSkillSetting("beta", {
    appConfig: fx.appConfig,
    skillRoots: [fx.skillRoot],
  });

  assert.equal(result.removed, true);
  assert.equal(result.removedFiles, true);
  assert.equal(fs.existsSync(path.join(fx.skillRoot, "beta")), false, "目录必须消失");
  // 再读一次必须真的读不到，否则等于没删
  const after = readSkillSettings({
    appConfig: fx.appConfig,
    repoRoot: fx.repoRoot,
    skillRoots: [fx.skillRoot],
  });
  assert.equal(
    after.some((s) => s.name === "beta"),
    false,
    "删除后重新读取不应再出现",
  );
});

test("删除不影响其它 skill，也不丢 config 里其它字段", () => {
  const fx = mkFixture();
  removeSkillSetting("beta", { appConfig: fx.appConfig, skillRoots: [fx.skillRoot] });

  const cfg = JSON.parse(fs.readFileSync(fx.appConfig, "utf8"));
  assert.equal(cfg.other_key, "must survive", "无关字段必须保留");
  assert.ok(cfg.skills.some((s) => s.name === "alpha"), "alpha 必须还在");
  // alpha 的全部字段都不能被顺手抹掉
  const alpha = cfg.skills.find((s) => s.name === "alpha");
  assert.deepEqual(alpha.tags, ["a", "tag"]);
  assert.deepEqual(alpha.triggers, ["触发A"]);
  assert.equal(alpha.priority, 70);
  assert.equal(alpha.core, true);
  assert.equal(alpha.category, "3D");
  assert.equal(fs.existsSync(path.join(fx.skillRoot, "alpha")), true, "alpha 目录必须保留");
});

test("deleteFiles=false 只摘注册项，并报告目录仍在", () => {
  const fx = mkFixture();
  const result = removeSkillSetting("beta", {
    appConfig: fx.appConfig,
    skillRoots: [fx.skillRoot],
    deleteFiles: false,
  });

  assert.equal(result.removed, true);
  assert.equal(result.removedFiles, false);
  assert.equal(result.dirRemaining, true, "必须告诉调用方目录还在，否则会被扫描捞回");
  assert.equal(fs.existsSync(path.join(fx.skillRoot, "beta")), true, "目录应保留");
});

test("删除未注册的磁盘 skill 也会清掉目录", () => {
  const fx = mkFixture();
  // gamma 只存在于磁盘，没有注册项
  const result = removeSkillSetting("gamma", {
    appConfig: fx.appConfig,
    skillRoots: [fx.skillRoot],
  });
  assert.equal(result.removedFiles, true);
  assert.equal(fs.existsSync(path.join(fx.skillRoot, "gamma")), false);
});

test("删除不存在的 skill 是幂等的", () => {
  const fx = mkFixture();
  const result = removeSkillSetting("nope", {
    appConfig: fx.appConfig,
    skillRoots: [fx.skillRoot],
  });
  assert.equal(result.removed, false);
  assert.equal(result.reason, "not found");
});

test("非法名字一律拒绝，防止路径穿越", () => {
  const fx = mkFixture();
  for (const bad of ["..", "../alpha", "a/b", ""]) {
    assert.throws(
      () => removeSkillSetting(bad, { appConfig: fx.appConfig, skillRoots: [fx.skillRoot] }),
      /skill name is required|invalid skill name/,
      `必须拒绝 ${JSON.stringify(bad)}`,
    );
  }
  assert.equal(fs.existsSync(path.join(fx.skillRoot, "alpha")), true, "alpha 不能被路径穿越删掉");
});
