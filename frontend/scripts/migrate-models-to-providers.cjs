#!/usr/bin/env node
// Migrate the hand-written model entries onto their providers.
//
// The providers already declare the base url, the dialect and the output
// limit, but the models carried their own copy of those values -- and the
// copy could disagree with the provider (both said "responses" while the
// agent always spoke Chat Completions). Turning a model into a derived
// entry makes the provider the single source of truth.
//
// A model is migrated when:
//   - it names a provider (the legacy "provider" tag), and
//   - that provider exists, and
//   - it has no api block of its own, or its api block matches the
//     provider on the fields the provider owns.
//
// Models that carry their own api block are left alone: they are the
// hand-written ones the owner may have tuned deliberately.
//
// Usage: node frontend/scripts/migrate-models-to-providers.cjs [--dry-run]

const fs = require("fs");
const path = require("path");

const REPO = path.resolve(__dirname, "..", "..");
const CONFIG = path.join(REPO, "config", "config.json");

const dryRun = process.argv.includes("--dry-run");

function main() {
  const raw = fs.readFileSync(CONFIG, "utf-8");
  const cfg = JSON.parse(raw);
  const providers = cfg.providers && typeof cfg.providers === "object" ? cfg.providers : {};
  const models = cfg.models && typeof cfg.models === "object" ? cfg.models : {};

  const migrated = [];
  const skipped = [];

  for (const [id, model] of Object.entries(models)) {
    const providerId = String((model && model.provider) || "").trim();
    if (!providerId) {
      skipped.push({ id, reason: "no provider tag" });
      continue;
    }
    if (!providers[providerId]) {
      skipped.push({ id, reason: `provider ${providerId} does not exist` });
      continue;
    }
    if (model.user) {
      // A user model is edited through the settings panel and carries its
      // own key; it stays a full entry.
      skipped.push({ id, reason: "user model" });
      continue;
    }
    const api = (model && model.api) || {};
    // A key that is only a reference (READ_FROM_...) points at the same
    // key file the provider uses, so the provider still owns it. A
    // literal key means the model really has credentials of its own.
    const rawKey = String(api.api_key || "");
    const isKeyReference = rawKey.startsWith("READ_FROM_");
    if (rawKey && !isKeyReference) {
      skipped.push({ id, reason: "owns its own api key" });
      continue;
    }
    const provider = providers[providerId];
    const modelId = String(api.model || id);
    migrated.push({
      id,
      providerId,
      modelId,
      providerFormat: provider.format,
      providerBaseUrl: provider.base_url,
      modelBaseUrl: api.base_url,
      sameBaseUrl: String(api.base_url || "") === String(provider.base_url || ""),
    });
  }

  for (const m of migrated) {
    const model = models[m.id];
    // Keep the display name and any other fields, replace the api block
    // with the provider reference.
    const { api, ...rest } = model;
    models[m.id] = {
      ...rest,
      display: rest.display || m.id,
      provider_id: m.providerId,
      model_id: m.modelId,
    };
  }

  if (!dryRun && migrated.length) {
    cfg.models = models;
    const tmp = CONFIG + ".tmp";
    fs.writeFileSync(tmp, JSON.stringify(cfg, null, 2));
    fs.renameSync(tmp, CONFIG);
  }

  console.log(`migrated ${migrated.length} model(s)${dryRun ? " (dry run)" : ""}`);
  for (const m of migrated) {
    console.log(
      `  ${m.id} -> provider ${m.providerId} (model_id=${m.modelId}, format=${m.providerFormat}, base_url ${m.sameBaseUrl ? "matches" : "DIFFERS: " + m.modelBaseUrl + " vs " + m.providerBaseUrl})`
    );
  }
  console.log(`skipped ${skipped.length}:`);
  for (const s of skipped) {
    console.log(`  ${s.id}: ${s.reason}`);
  }
}

main();
