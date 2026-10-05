import { readFileSync, readdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const systemDir = path.join(repoRoot, "config", "system");
const manifestPath = path.join(systemDir, "manifest.yml");

function parseManifest(source) {
  const sections = new Map();
  let language = "";
  for (const [index, rawLine] of source.replace(/\r\n?/g, "\n").split("\n").entries()) {
    const line = rawLine.replace(/\s+#.*$/, "").trim();
    if (!line || line.startsWith("#")) continue;
    const section = line.match(/^([a-z][a-z0-9_-]*):$/i);
    if (section) {
      language = section[1];
      if (sections.has(language)) throw new Error(`duplicate language ${language} at line ${index + 1}`);
      sections.set(language, []);
      continue;
    }
    const item = line.match(/^-\s+([^\s]+\.md)$/);
    if (!item || !language) throw new Error(`invalid manifest line ${index + 1}: ${rawLine}`);
    if (path.basename(item[1]) !== item[1]) throw new Error(`manifest entries must be filenames: ${item[1]}`);
    if (sections.get(language).includes(item[1])) throw new Error(`duplicate entry for ${language}: ${item[1]}`);
    sections.get(language).push(item[1]);
  }
  if (sections.size === 0) throw new Error("system prompt manifest is empty");
  return sections;
}

function normalizedPart(filePath) {
  return readFileSync(filePath, "utf8")
    .replace(/^\uFEFF/, "")
    .replace(/\r\n?/g, "\n")
    .replace(/\n+$/, "");
}

function assemble(language, entries) {
  const partsDir = path.join(systemDir, "parts", language);
  const actual = readdirSync(partsDir)
    .filter(name => name.endsWith(".md"))
    .sort();
  const declared = [...entries].sort();
  const missing = declared.filter(name => !actual.includes(name));
  const undeclared = actual.filter(name => !declared.includes(name));
  if (missing.length || undeclared.length) {
    throw new Error(
      `${language} manifest mismatch; missing=[${missing.join(", ")}], undeclared=[${undeclared.join(", ")}]`,
    );
  }
  const body = entries.map(name => normalizedPart(path.join(partsDir, name))).join("\n\n") + "\n";
  return `Today: {{ CURRENT_TIME }}\n\n${body}`;
}

export function buildSystemPrompts({ check = false } = {}) {
  const manifest = parseManifest(readFileSync(manifestPath, "utf8"));
  const results = [];
  for (const [language, entries] of manifest) {
    const outputPath = path.join(systemDir, `${language}.md`);
    const expected = assemble(language, entries);
    if (check) {
      let actual = "";
      try {
        actual = readFileSync(outputPath, "utf8").replace(/^\uFEFF/, "").replace(/\r\n?/g, "\n");
      } catch {
        throw new Error(`missing generated system prompt: ${path.relative(repoRoot, outputPath)}`);
      }
      if (actual !== expected) {
        throw new Error(
          `${path.relative(repoRoot, outputPath)} is stale; run pnpm prompt:build`,
        );
      }
    } else {
      writeFileSync(outputPath, expected, "utf8");
    }
    results.push(path.relative(repoRoot, outputPath));
  }
  return results;
}

const check = process.argv.includes("--check");
try {
  const results = buildSystemPrompts({ check });
  console.log(`[system-prompts] ${check ? "validated" : "built"}: ${results.join(", ")}`);
} catch (error) {
  console.error(`[system-prompts] ${error.message}`);
  process.exitCode = 1;
}
