export const UNCATEGORIZED = '未分类';

export function skillCategory(skill) {
  return (skill && skill.category) || UNCATEGORIZED;
}

/**
 * 两层技能树：
 *   第一层：核心技能（core=true，置顶，独立成组）
 *   第二层：大分类（其余技能按 category 分组，未分类沉底）
 *
 * 核心技能不会再在分类里重复出现；取消核心后它会回到自己原来的分类，
 * 所以这里的 core 只影响位置，不改 category 字段。
 */
export function buildSkillTree(skills) {
  const core = [];
  const buckets = new Map();
  for (const skill of Array.isArray(skills) ? skills : []) {
    if (!skill || !skill.name) continue;
    if (skill.core) {
      core.push(skill);
      continue;
    }
    const category = skillCategory(skill);
    if (!buckets.has(category)) buckets.set(category, []);
    buckets.get(category).push(skill);
  }
  const byName = (a, b) => String(a.name).localeCompare(String(b.name));
  const groups = [...buckets.entries()]
    .map(([category, items]) => ({ category, skills: [...items].sort(byName) }))
    .sort((a, b) => {
      if (a.category === UNCATEGORIZED) return 1;
      if (b.category === UNCATEGORIZED) return -1;
      return a.category.localeCompare(b.category);
    });
  core.sort(byName);
  return { core, groups };
}
