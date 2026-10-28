/**
 * One-off migration: introduce the providers section in config.json.
 *
 * The existing models keep the exact key references they had. A provider
 * points at the same key file, so no key is copied, moved or rewritten and
 * nothing that already worked can break.
 *
 * The provider is what the settings panel groups by, and what "fetch the
 * available models" uses. The per model key stays per model.
 */
const fs = require("fs");
const path = require("path");

const REPO = path.resolve(__dirname, "..", "..");
const CONFIG = path.join(REPO, "config", "config.json");

function readJson(file, fallback) {
  try {
    return JSON.parse(fs.readFileSync(file, "utf8"));
  } catch {
    return fallback;
  }
}

function writeJson(file, data) {
  fs.writeFileSync(file, `${JSON.stringify(data, null, 2)}\n`, "utf8");
}

const cfg = readJson(CONFIG, null);
if (!cfg) {
  console.error("config.json 读不到，终止");
  process.exit(1);
}

const providers = {
  minimax: {
    name: "MiniMax",
    note: "官方",
    homepage: "https://www.minimaxi.com",
    base_url: "https://api.minimaxi.com/v1",
    format: "chat_completions",
    output_limit_field: "max_tokens",
    output_limit: 16384,
    // The provider reuses the key file the models already read, so the
    // credentials are not duplicated anywhere.
    api_key_ref: "READ_FROM_MINIMAX_KEY_TXT",
    models: ["minimax-m3", "user_minimax-m3-1-flash"],
  },
  deepseek: {
    name: "DeepSeek",
    note: "官方",
    homepage: "https://www.deepseek.com",
    base_url: "https://api.deepseek.com",
    format: "chat_completions",
    output_limit_field: "max_tokens",
    output_limit: 16384,
    api_key_ref: "READ_FROM_DEEPSEEK_KEY_TXT",
    models: ["deepseek-v4-flash"],
  },
};

cfg.providers = providers;

// The deepseek model had no provider field, so it was invisible in a
// provider grouped view. The minimax one already named "minimax".
if (cfg.models && cfg.models["deepseek-v4-flash"] && !cfg.models["deepseek-v4-flash"].provider) {
  cfg.models["deepseek-v4-flash"].provider = "deepseek";
}
// The official minimax model also had no provider field.
if (cfg.models && cfg.models["minimax-m3"] && !cfg.models["minimax-m3"].provider) {
  cfg.models["minimax-m3"].provider = "minimax";
}

writeJson(CONFIG, cfg);
console.log("providers 段已写入 config.json");
for (const [id, p] of Object.entries(providers)) {
  console.log(`  ${id}: ${p.name} (${p.models.length} 个模型) key=${p.api_key_ref}`);
}
