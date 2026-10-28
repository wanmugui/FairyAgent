const test = require("node:test");
const assert = require("node:assert");
const fs = require("fs");
const os = require("os");
const path = require("path");

const { ProviderRegistry, normalizeProvider, maskApiKey } = require("./provider-registry.cjs");

/** A registry over a throwaway config and secrets file. */
function makeRegistry(config = {}, secrets = {}) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "provider-reg-"));
  const configPath = path.join(root, "config.json");
  const secretsPath = path.join(root, "local_secrets.json");
  fs.writeFileSync(configPath, JSON.stringify(config, null, 2));
  fs.writeFileSync(secretsPath, JSON.stringify(secrets, null, 2));
  return new ProviderRegistry(configPath, secretsPath);
}

test("maskApiKey keeps the ends and hides the middle", () => {
  const key = "sk-1234567890abcdef";
  const masked = maskApiKey(key);
  // The first and last four characters stay readable, the rest is hidden.
  assert.ok(masked.startsWith("sk-1"));
  assert.ok(masked.endsWith("cdef"));
  assert.ok(masked.includes("•"));
  assert.ok(masked.length < key.length + 4);
  assert.strictEqual(maskApiKey("short"), "•••••");
  assert.strictEqual(maskApiKey(""), "");
});

test("normalizeProvider drops an unknown upstream format", () => {
  const provider = normalizeProvider("p", { name: "P", format: "grpc" });
  assert.strictEqual(provider.format, "chat_completions");
  assert.strictEqual(provider.output_limit_field, "max_tokens");
});

test("normalizeProvider picks the output limit field per dialect", () => {
  const responses = normalizeProvider("p", { format: "responses" });
  assert.strictEqual(responses.output_limit_field, "max_output_tokens");
  const chat = normalizeProvider("p", { format: "chat_completions" });
  assert.strictEqual(chat.output_limit_field, "max_tokens");
});

test("a provider key is stored in the secrets file, not in the config", () => {
  const registry = makeRegistry();
  registry.save("acme", { name: "Acme", base_url: "https://acme.test/v1", api_key: "sk-secret-value" });

  const config = JSON.parse(fs.readFileSync(registry.configPath, "utf8"));
  const secrets = JSON.parse(fs.readFileSync(registry.secretsPath, "utf8"));
  // The tracked config only holds a reference.
  assert.ok(!JSON.stringify(config).includes("sk-secret-value"));
  assert.strictEqual(config.providers.acme.api_key_ref, "READ_FROM_LOCAL_SECRETS:provider_acme");
  assert.strictEqual(secrets.provider_acme, "sk-secret-value");
});

test("a provider is readable back with its key resolved", () => {
  const registry = makeRegistry();
  registry.save("acme", { name: "Acme", base_url: "https://acme.test/v1", api_key: "sk-secret-value" });
  assert.strictEqual(registry.apiKey("acme"), "sk-secret-value");
  const shown = registry.listForDisplay();
  assert.strictEqual(shown.length, 1);
  assert.strictEqual(shown[0].has_key, true);
  assert.ok(!("api_key" in shown[0]), "the key must not be returned by default");
  assert.strictEqual(shown[0].api_key_masked, maskApiKey("sk-secret-value"));
});

test("reveal returns the plaintext key", () => {
  const registry = makeRegistry();
  registry.save("acme", { name: "Acme", api_key: "sk-secret-value" });
  const shown = registry.listForDisplay({ reveal: true });
  assert.strictEqual(shown[0].api_key, "sk-secret-value");
});

test("saving without a key keeps the stored one", () => {
  const registry = makeRegistry();
  registry.save("acme", { name: "Acme", api_key: "sk-secret-value" });
  // The form shows a masked key, so a save must not wipe the real one.
  registry.save("acme", { name: "Acme Renamed" });
  assert.strictEqual(registry.apiKey("acme"), "sk-secret-value");
  assert.strictEqual(registry.get("acme").name, "Acme Renamed");
});

test("a provider cannot be pointed at another provider's secret", () => {
  const registry = makeRegistry();
  registry.save("acme", { name: "Acme", api_key: "sk-acme" });
  registry.save("other", { name: "Other", api_key: "sk-other" });
  // The ref is derived from the id, so a client cannot redirect it.
  registry.save("acme", { api_key_ref: "READ_FROM_LOCAL_SECRETS:provider_other" });
  assert.strictEqual(registry.apiKey("acme"), "sk-acme");
});

test("removing a provider removes its key too", () => {
  const registry = makeRegistry();
  registry.save("acme", { name: "Acme", api_key: "sk-secret-value" });
  assert.strictEqual(registry.remove("acme"), true);
  const secrets = JSON.parse(fs.readFileSync(registry.secretsPath, "utf8"));
  assert.ok(!("provider_acme" in secrets));
  assert.strictEqual(registry.get("acme"), null);
});

test("models are grouped by the provider that names them", () => {
  const registry = makeRegistry({
    providers: {
      acme: { name: "Acme", base_url: "https://acme.test/v1" },
      other: { name: "Other", base_url: "https://other.test/v1" },
    },
    models: {
      "m1": { provider: "acme", api: { base_url: "https://acme.test/v1" } },
      "m2": { api: { base_url: "https://acme.test/v1" } },
      "m3": { provider: "other", api: { base_url: "https://other.test/v1" } },
      "m4": { api: { base_url: "https://unknown.test/v1" } },
    },
  });
  const byProvider = registry.modelsByProvider();
  // m1 names acme, m2 matches by base url, m4 belongs to nobody.
  assert.deepStrictEqual(byProvider.acme.sort(), ["m1", "m2"]);
  assert.deepStrictEqual(byProvider.other, ["m3"]);
  assert.ok(!byProvider.acme.includes("m4"));
});

test("models grouped by provider_id drive the provider model count", () => {
  const registry = makeRegistry({
    providers: {
      minimax: { name: "MiniMax", base_url: "https://api.minimaxi.com/v1" },
    },
    models: {
      "minimax-m3": { provider_id: "minimax", model_id: "MiniMax-M3" },
      "user_minimax-m3-1-flash": {
        provider_id: "minimax",
        model_id: "MiniMax-M3.1-Flash-Preview",
      },
    },
  });

  assert.deepStrictEqual(registry.modelsByProvider().minimax, [
    "minimax-m3",
    "user_minimax-m3-1-flash",
  ]);
  assert.strictEqual(registry.listForDisplay()[0].model_count, 2);
});

test("a model naming a provider without an entry still groups", () => {
  // A model added before the provider existed has no provider entry, and
  // the panel still has to show it under the name the model carries.
  const registry = makeRegistry({
    models: { m1: { provider: "ghost", api: {} } },
  });
  const byProvider = registry.modelsByProvider();
  assert.deepStrictEqual(byProvider.ghost, ["m1"]);
});

test("a provider keeps a key file reference when no new key is given", () => {
  // A migrated provider reads its key from a file in the repository. A save
  // that carries no new key must not repoint it at the secrets file.
  const registry = makeRegistry({
    providers: { acme: { name: "Acme", api_key_ref: "READ_FROM_ACME_KEY_TXT" } },
  });
  registry.save("acme", { name: "Acme Renamed" });
  const provider = registry.get("acme");
  assert.strictEqual(provider.api_key_ref, "READ_FROM_ACME_KEY_TXT");
});

test("a provider reads its key from a key file in the repository", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "provider-key-"));
  fs.writeFileSync(path.join(root, "ACME_KEY_TXT.txt"), "# a comment\nsk-from-file\n");
  const configPath = path.join(root, "config.json");
  const secretsPath = path.join(root, "local_secrets.json");
  fs.writeFileSync(
    configPath,
    JSON.stringify({ providers: { acme: { name: "Acme", api_key_ref: "READ_FROM_ACME_KEY_TXT" } } })
  );
  fs.writeFileSync(secretsPath, "{}");
  const registry = new ProviderRegistry(configPath, secretsPath, root);
  // The comment line is skipped and the first real line is the key.
  assert.strictEqual(registry.apiKey("acme"), "sk-from-file");
});

test("a masked key does not overwrite the stored one", () => {
  // The form shows a masked key. Saving the form must not replace the real
  // key with the mask.
  const registry = makeRegistry();
  registry.save("acme", { name: "Acme", api_key: "sk-real-key" });
  registry.save("acme", { name: "Acme", api_key: maskApiKey("sk-real-key") });
  assert.strictEqual(registry.apiKey("acme"), "sk-real-key");
});

test("the model count is reported per provider", () => {
  const registry = makeRegistry({
    providers: { acme: { name: "Acme", base_url: "https://acme.test/v1" } },
    models: { m1: { provider: "acme", api: {} }, m2: { provider: "acme", api: {} } },
  });
  const shown = registry.listForDisplay();
  assert.strictEqual(shown[0].model_count, 2);
});

// --- commitModels / deriveModels -----------------------------------------

test("commitModels adds the models and derives the model table", () => {
  const registry = makeRegistry({
    providers: { acme: { name: "Acme", base_url: "https://acme.test/v1" } },
  });
  const result = registry.commitModels("acme", ["m1", "m2"]);
  assert.deepStrictEqual(result.added, ["m1", "m2"]);
  assert.strictEqual(result.ok, true);

  const cfg = registry.readConfig();
  assert.deepStrictEqual(cfg.providers.acme.models, ["m1", "m2"]);
  // The derived model points at the provider instead of carrying its own
  // api block.
  assert.strictEqual(cfg.models.m1.provider_id, "acme");
  assert.strictEqual(cfg.models.m1.model_id, "m1");
  assert.strictEqual(cfg.models.m1.api, undefined);
});

test("a model that is already configured keeps its own key and model id", () => {
  // The provider's list holds config keys. Deriving must not overwrite the
  // upstream model id of an entry that already declares one: the config
  // key "acme-m1" is a friendly name for the upstream "Acme-M1".
  const registry = makeRegistry({
    providers: { acme: { name: "Acme", models: ["acme-m1"] } },
    models: {
      "acme-m1": { display: "Acme M1", provider_id: "acme", model_id: "Acme-M1" },
    },
  });
  registry.save("acme", { name: "Acme", models: ["acme-m1"] });
  const cfg = registry.readConfig();
  assert.strictEqual(Object.keys(cfg.models).length, 1);
  assert.strictEqual(cfg.models["acme-m1"].model_id, "Acme-M1");
  assert.strictEqual(cfg.models["acme-m1"].display, "Acme M1");
});

test("deriveModels preserves a legacy api.model as the upstream model id", () => {
  const registry = makeRegistry({
    providers: { acme: { name: "Acme", models: ["acme-m1"] } },
    models: {
      "acme-m1": {
        display: "Acme M1",
        api: { model: "Acme-M1", base_url: "https://acme.test/v1" },
      },
    },
  });

  registry.save("acme", { name: "Acme", models: ["acme-m1"] });
  const cfg = registry.readConfig();

  assert.strictEqual(cfg.models["acme-m1"].model_id, "Acme-M1");
});

test("commitModels keeps the models that are already there", () => {
  const registry = makeRegistry({
    providers: { acme: { name: "Acme", models: ["m1"] } },
  });
  const result = registry.commitModels("acme", ["m1", "m2"]);
  assert.deepStrictEqual(result.added, ["m2"]);
  assert.deepStrictEqual(result.existing, ["m1"]);
  assert.deepStrictEqual(registry.readConfig().providers.acme.models, ["m1", "m2"]);
});

test("commitModels does not let one provider steal another's model", () => {
  const registry = makeRegistry({
    providers: {
      acme: { name: "Acme" },
      other: { name: "Other" },
    },
    models: { shared: { provider_id: "acme", model_id: "shared" } },
  });
  const result = registry.commitModels("other", ["shared"]);
  assert.strictEqual(result.added.length, 0);
  assert.strictEqual(result.conflicts.length, 1);
  assert.strictEqual(result.conflicts[0].model, "shared");
  // The original owner keeps it.
  assert.strictEqual(registry.readConfig().models.shared.provider_id, "acme");
});

test("a hand written model survives the derivation", () => {
  const registry = makeRegistry({
    providers: { acme: { name: "Acme", models: ["m1"] } },
    models: {
      legacy: { display: "Legacy", api: { base_url: "https://old.test/v1", model: "old" } },
    },
  });
  registry.save("acme", { name: "Acme" });
  const cfg = registry.readConfig();
  // The legacy entry keeps its own api block.
  assert.strictEqual(cfg.models.legacy.api.base_url, "https://old.test/v1");
  assert.strictEqual(cfg.models.m1.provider_id, "acme");
});

test("saving a provider derives its models too", () => {
  const registry = makeRegistry();
  registry.save("acme", { name: "Acme", models: ["m1", "m2"] });
  const cfg = registry.readConfig();
  assert.strictEqual(cfg.models.m1.provider_id, "acme");
  assert.strictEqual(cfg.models.m2.provider_id, "acme");
});

test("anthropic messages is an accepted upstream format", () => {
  const provider = normalizeProvider("p", { format: "anthropic_messages" });
  assert.strictEqual(provider.format, "anthropic_messages");
  // Anthropic carries the output limit in max_tokens, like Chat Completions.
  assert.strictEqual(provider.output_limit_field, "max_tokens");
});
