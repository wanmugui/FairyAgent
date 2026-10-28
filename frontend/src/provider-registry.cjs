/**
 * Provider registry.
 *
 * A provider is the service an API key belongs to: one base url, one key,
 * one upstream dialect. Several models share a provider, and every model
 * under it is reachable through the same credentials.
 *
 * The registry lives in config.json next to the models it serves, so the
 * two halves of the configuration stay in one file. The API key itself is
 * kept in config/local_secrets.json and is referenced from config.json the
 * same way a model references its key, so no plaintext key is ever written
 * into the tracked configuration.
 */
const fs = require("fs");
const path = require("path");

/** The upstream dialects Fairy can talk to. */
const UPSTREAM_FORMATS = ["chat_completions", "responses", "anthropic_messages"];

/** The field that carries the output limit, per upstream dialect. */
const OUTPUT_LIMIT_FIELDS = ["max_tokens", "max_output_tokens"];

/**
 * Normalize a provider entry.
 *
 * Unknown fields are dropped and missing ones get a default, so a hand
 * edited config.json cannot push a malformed value into the request path.
 */
function normalizeProvider(id, raw) {
  const source = raw && typeof raw === "object" ? raw : {};
  const format = UPSTREAM_FORMATS.includes(source.format) ? source.format : "chat_completions";
  const limitField = OUTPUT_LIMIT_FIELDS.includes(source.output_limit_field)
    ? source.output_limit_field
    : format === "responses"
      ? "max_output_tokens"
      : "max_tokens";
  return {
    id: String(id || "").trim(),
    name: String(source.name || id || "").trim(),
    note: String(source.note || ""),
    homepage: String(source.homepage || ""),
    base_url: String(source.base_url || "").trim().replace(/\/+$/, ""),
    format,
    output_limit_field: limitField,
    output_limit: Number(source.output_limit) || 0,
    api_key_ref: String(source.api_key_ref || ""),
    models: Array.isArray(source.models) ? source.models.map(String) : [],
  };
}

/**
 * Mask an API key for display.
 *
 * A short key is masked completely, a longer one keeps its first and last
 * four characters, so the owner can tell two keys apart without the value
 * being usable.
 */
function maskApiKey(key) {
  const text = String(key || "").trim();
  if (!text) return "";
  if (text.length <= 8) return "•".repeat(text.length);
  return `${text.slice(0, 4)}${"•".repeat(Math.max(4, text.length - 8))}${text.slice(-4)}`;
}

class ProviderRegistry {
  /**
   * @param {string} configPath  path of config.json
   * @param {string} secretsPath path of config/local_secrets.json
   * @param {string} repoRoot    repository root, for the key file references
   */
  constructor(configPath, secretsPath, repoRoot) {
    this.configPath = configPath;
    this.secretsPath = secretsPath;
    this.repoRoot = repoRoot || path.dirname(path.dirname(configPath));
  }

  readConfig() {
    try {
      const text = fs.readFileSync(this.configPath, "utf8");
      const cfg = JSON.parse(text);
      return cfg && typeof cfg === "object" ? cfg : {};
    } catch {
      return {};
    }
  }

  writeConfig(cfg) {
    fs.writeFileSync(this.configPath, `${JSON.stringify(cfg, null, 2)}\n`, "utf8");
  }

  readSecrets() {
    try {
      const text = fs.readFileSync(this.secretsPath, "utf8");
      const data = JSON.parse(text);
      return data && typeof data === "object" ? data : {};
    } catch {
      return {};
    }
  }

  writeSecrets(secrets) {
    fs.writeFileSync(this.secretsPath, `${JSON.stringify(secrets, null, 2)}\n`, "utf8");
  }

  /** The secret id a provider stores its key under. */
  secretId(id) {
    return `provider_${String(id || "").trim()}`;
  }

  /** All providers, normalized. */
  list() {
    const cfg = this.readConfig();
    const providers = cfg.providers && typeof cfg.providers === "object" ? cfg.providers : {};
    return Object.entries(providers).map(([id, raw]) => normalizeProvider(id, raw));
  }

  get(id) {
    const wanted = String(id || "").trim();
    return this.list().find((provider) => provider.id === wanted) || null;
  }

  /**
   * The file names a READ_FROM_ reference may point at.
   *
   * The suffix is a hint, not a promise: READ_FROM_MINIMAX_KEY_TXT also
   * resolves to MINIMAX_key.txt and MINIMAX.txt, because the owner renamed
   * the file at some point and the configuration still points at the old
   * name.
   */
  keyFileCandidates(ref) {
    const raw = String(ref || "").trim();
    if (!raw.startsWith("READ_FROM_")) return [];
    const suffix = raw.slice("READ_FROM_".length);
    const candidates = [`${suffix}.txt`];
    if (suffix.endsWith("_KEY_TXT")) {
      const base = suffix.slice(0, -"_KEY_TXT".length);
      candidates.push(`${base}_key.txt`, `${base}.txt`);
    }
    return candidates;
  }

  /** The plaintext key of a provider, or an empty string. */
  apiKey(id) {
    const provider = this.get(id);
    if (!provider) return "";
    const secrets = this.readSecrets();
    const stored = secrets[this.secretId(provider.id)];
    if (stored) return String(stored);
    if (!provider.api_key_ref) return "";
    const ref = provider.api_key_ref;
    if (ref.startsWith("READ_FROM_LOCAL_SECRETS:")) {
      const key = ref.slice("READ_FROM_LOCAL_SECRETS:".length);
      return String(secrets[key] || "");
    }
    if (!ref.startsWith("READ_FROM_")) return ref;
    // A reference to a key file in the repository.
    for (const name of this.keyFileCandidates(ref)) {
      let text = "";
      try {
        text = fs.readFileSync(path.join(this.repoRoot, name), "utf8");
      } catch {
        continue;
      }
      const first = text
        .split(/\r?\n/)
        .map((line) => line.trim())
        .find((line) => line && !line.startsWith("#"));
      if (first && !first.startsWith("READ_FROM_")) return first;
    }
    return "";
  }

  /**
   * The provider list for the settings panel.
   *
   * The key comes back masked. Passing reveal returns the plaintext, which
   * the owner asked for: the settings page is where a key is checked, and a
   * masked value cannot be verified against the provider's dashboard.
   */
  listForDisplay(options = {}) {
    const models = this.modelsByProvider();
    return this.list().map((provider) => {
      const key = this.apiKey(provider.id);
      const item = {
        ...provider,
        has_key: !!key,
        api_key_masked: maskApiKey(key),
        model_count: (models[provider.id] || []).length,
      };
      if (options.reveal) item.api_key = key;
      return item;
    });
  }

  /**
   * The model ids that belong to each provider.
   *
   * A model belongs to the provider whose base url matches, and to the
   * provider it names explicitly. Both are checked, because a model added
   * before the provider existed has no provider of its own.
   */
  modelsByProvider() {
    const cfg = this.readConfig();
    const models = cfg.models && typeof cfg.models === "object" ? cfg.models : {};
    const providers = this.list();
    const byProvider = {};
    for (const provider of providers) byProvider[provider.id] = [];

    for (const [id, model] of Object.entries(models)) {
      const api = (model && model.api) || {};
      const named = String(
        (model && (model.provider_id || model.provider)) || ""
      ).trim();
      let owner = named;
      if (!owner) {
        const base = String(api.base_url || "").trim().replace(/\/+$/, "");
        const match = providers.find(
          (provider) => provider.base_url && provider.base_url === base
        );
        if (match) owner = match.id;
      }
      // A model can name a provider that has no entry yet, so the bucket is
      // created on demand rather than only for the known providers.
      if (owner) {
        if (!byProvider[owner]) byProvider[owner] = [];
        byProvider[owner].push(id);
      }
    }
    return byProvider;
  }

  /**
   * Create or update a provider.
   *
   * The key is stored in local_secrets.json. An empty key on update keeps
   * the stored one, so saving a form that shows a masked key does not wipe
   * it.
   */
  save(id, input = {}) {
    const providerId = String(id || input.id || "").trim();
    if (!providerId) throw new Error("供应商 id 不能为空");
    const cfg = this.readConfig();
    const providers = cfg.providers && typeof cfg.providers === "object" ? cfg.providers : {};
    const existing = providers[providerId] ? normalizeProvider(providerId, providers[providerId]) : null;

    // A new key is stored in local_secrets.json and referenced from there.
    // An existing reference is kept when the form did not carry a new key,
    // so a provider that reads a key file keeps reading that file.
    // A masked value counts as "no new key": the form shows a masked key
    // and saving it must not overwrite the real one with the mask.
    const submitted = String(input.api_key || "").trim();
    const newKey = submitted.includes("•") ? "" : submitted;
    const apiKeyRef = newKey
      ? `READ_FROM_LOCAL_SECRETS:${this.secretId(providerId)}`
      : (existing && existing.api_key_ref) || "";

    const provider = normalizeProvider(providerId, {
      ...(existing || {}),
      ...input,
      id: providerId,
      // The key reference is derived, not taken from the input, so a client
      // cannot point a provider at another provider's secret.
      api_key_ref: apiKeyRef,
    });

    providers[providerId] = {
      name: provider.name,
      note: provider.note,
      homepage: provider.homepage,
      base_url: provider.base_url,
      format: provider.format,
      output_limit_field: provider.output_limit_field,
      output_limit: provider.output_limit,
      api_key_ref: provider.api_key_ref,
      models: provider.models,
    };
    cfg.providers = providers;

    // Saving a provider also re-derives the model table, so a model added
    // by hand to the provider's list becomes selectable right away.
    const conflicts = [];
    cfg.models = this.deriveModels(providers, cfg.models, conflicts);
    this.writeConfig(cfg);

    const key = newKey;
    if (key) {
      const secrets = this.readSecrets();
      secrets[this.secretId(providerId)] = key;
      this.writeSecrets(secrets);
    }

    return provider;
  }

  /**
   * Add models to a provider and derive the model table from it.
   *
   * This is what makes "拉取可用模型" actually useful: the models the
   * upstream offers are merged into the provider, and the config's model
   * table is regenerated from the providers. A model that another provider
   * already owns is not stolen -- the conflict is reported instead, so
   * two providers can legitimately offer the same model name.
   *
   * @param {string} id            provider id
   * @param {string[]} modelNames  model ids to add
   * @returns {{ok:boolean, added:string[], existing:string[], conflicts:string[], models:string[]}}
   */
  commitModels(id, modelNames = []) {
    const providerId = String(id || "").trim();
    if (!providerId) throw new Error("供应商 id 不能为空");
    const cfg = this.readConfig();
    const providers = cfg.providers && typeof cfg.providers === "object" ? cfg.providers : {};
    if (!providers[providerId]) throw new Error("供应商不存在");

    const provider = normalizeProvider(providerId, providers[providerId]);
    const current = Array.isArray(provider.models) ? provider.models.slice() : [];
    const have = new Set(current);

    const added = [];
    const existing = [];
    const conflicts = [];
    // The model table is keyed the same way the provider's list is, so a
    // key another provider already owns is a conflict. A model another
    // provider owns is reported and NOT added to this provider's list --
    // listing it here while the table says otherwise would leave the two
    // out of sync.
    const currentModels = cfg.models && typeof cfg.models === "object" ? cfg.models : {};
    for (const raw of Array.isArray(modelNames) ? modelNames : []) {
      const name = String(raw || "").trim();
      if (!name) continue;
      if (have.has(name)) {
        existing.push(name);
        continue;
      }
      const owner = String((currentModels[name] && currentModels[name].provider_id) || "").trim();
      if (owner && owner !== providerId) {
        conflicts.push({ model: name, provider: providerId, existing_provider: owner });
        continue;
      }
      have.add(name);
      current.push(name);
      added.push(name);
    }

    providers[providerId] = { ...providers[providerId], models: current };
    cfg.providers = providers;

    // Re-derive the model table so the new entries become selectable.
    const derived = this.deriveModels(providers, cfg.models, conflicts);
    cfg.models = derived;

    this.writeConfig(cfg);
    return {
      ok: true,
      added,
      existing,
      conflicts,
      models: current,
    };
  }

  /**
   * Rebuild the model table from the providers.
   *
   * A derived model carries only what is model-specific (its id, display
   * name and the model id sent upstream). The base url, key, dialect and
   * output limit come from the owning provider at request time, so the
   * same values are not duplicated per model.
   *
   * Models that were configured by hand (they still carry their own api
   * block) are kept as they are: the config may predate the provider
   * registry, and silently dropping those would break existing setups.
   */
  deriveModels(providers, existingModels = {}, conflicts = []) {
    const models = existingModels && typeof existingModels === "object" ? { ...existingModels } : {};

    // The provider's model list holds the config keys of the models it
    // serves ("minimax-m3"), because that is how the owner named them.
    // A model that was pulled from the upstream is added under the
    // upstream name, which is the name the request must carry.
    //
    // A key may already exist: it is then updated in place, so the owner's
    // key and display name survive.
    for (const [pid, raw] of Object.entries(providers || {})) {
      const p = normalizeProvider(pid, raw);
      for (const key of p.models || []) {
        const modelKey = String(key || "").trim();
        if (!modelKey) continue;
        const current = models[modelKey];
        if (current) {
          // Only provider_id is a reference. The legacy "provider" field
          // is an ownership tag, so a hand-written model that names its
          // provider is not treated as a conflict with itself.
          const owner = String(current.provider_id || "").trim();
          if (owner && owner !== pid) {
            conflicts.push({ model: modelKey, provider: pid, existing_provider: owner });
            continue; // do not steal a model another provider owns
          }
        }
        models[modelKey] = {
          id: modelKey,
          // Keep the display name the entry already had.
          name: (current && (current.name || current.display)) || modelKey,
          display: (current && (current.display || current.name)) || modelKey,
          // provider_id is the reference the agent resolves at request
          // time. The older "provider" field is only an ownership tag and
          // is deliberately not set here: the agent treats it as a tag,
          // not as a source of connection details.
          provider_id: pid,
          // The model id sent upstream. An entry that already declares one
          // keeps it: the config key may be a friendly name whose upstream
          // name differs ("minimax-m3" -> "MiniMax-M3").
          model_id:
            (current && current.model_id) ||
            (current && current.api && current.api.model) ||
            modelKey,
        };
      }
    }
    return models;
  }

  /** Remove a provider and the key it owns. */
  remove(id) {
    const providerId = String(id || "").trim();
    const cfg = this.readConfig();
    if (!cfg.providers || !(providerId in cfg.providers)) return false;
    delete cfg.providers[providerId];
    cfg.providers = cfg.providers;
    this.writeConfig(cfg);
    const secrets = this.readSecrets();
    const key = this.secretId(providerId);
    if (key in secrets) {
      delete secrets[key];
      this.writeSecrets(secrets);
    }
    return true;
  }
}

module.exports = {
  ProviderRegistry,
  UPSTREAM_FORMATS,
  OUTPUT_LIMIT_FIELDS,
  normalizeProvider,
  maskApiKey,
};
