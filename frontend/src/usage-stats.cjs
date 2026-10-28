/**
 * Usage statistics over the durable per-turn usage ledger.
 *
 * Every session directory holds a usage.json with a "turns" array. Each turn
 * is one model request:
 *
 *   { step, prompt_tokens, completion_tokens, duration_ms, real_ms,
 *     message_index, cached_tokens?, reasoning_tokens?, model?, provider? }
 *
 * The token counters are the ones the upstream provider reported. The
 * optional fields are only present when the provider supplied them, which is
 * why every consumer has to treat them as absent rather than zero.
 *
 * Historical turns predate the model field, so the model id is backfilled
 * from the session file when a turn does not carry one.
 */
const fs = require("fs");
const path = require("path");

/**
 * Price table for cost estimation. Rates are US dollars per million tokens.
 *
 * Rates were taken from the vendor pricing pages. MiniMax-M3 publishes a
 * long-context tier: requests whose prompt exceeds 512k tokens are billed at
 * 1.5x, so the tier has to be honoured rather than averaged away. The
 * long-context rows are marked with longContext and are applied to the part
 * of the prompt above the threshold.
 *
 * A provider that is not listed here has no price, and its requests are
 * reported as "unpriced" rather than as zero cost. Reporting zero would look
 * like a free request, which is worse than saying nothing is known.
 */
const DEFAULT_PRICING = {
  // model id (lowercase) -> { prompt, completion, cachedPrompt, longContext? }
  "minimax-m3": {
    prompt: 0.6,
    completion: 4.6,
    cachedPrompt: 0.6,
    longContext: { overTokens: 512_000, prompt: 1.6, completion: 6.9 },
  },
  // The Flash preview has no published price, so it stays unpriced rather
  // than being guessed at.
  "minimax-m3.1-flash-preview": null,
  "minimax-m3.1": null,
  "minimax-text-01": null,
  "deepseek-chat": { prompt: 0.27, completion: 1.1, cachedPrompt: 0.07 },
  "deepseek-reasoner": { prompt: 0.55, completion: 2.19, cachedPrompt: 0.14 },
  "gpt-4o": { prompt: 2.5, completion: 10, cachedPrompt: 1.25 },
  "gpt-4o-mini": { prompt: 0.15, completion: 0.6, cachedPrompt: 0.075 },
  "claude-3-5-sonnet": { prompt: 3, completion: 15, cachedPrompt: 0.3 },
  "claude-3-5-haiku": { prompt: 0.8, completion: 4, cachedPrompt: 0.08 },
};

function readJsonSafe(file, fallback) {
  try {
    return JSON.parse(fs.readFileSync(file, "utf8"));
  } catch {
    return fallback;
  }
}

/** Lowercase a model id and strip a provider prefix so pricing lookups match. */
function pricingKey(modelId) {
  const raw = String(modelId || "").trim().toLowerCase();
  if (!raw) return "";
  const withoutPrefix = raw.includes("/") ? raw.slice(raw.lastIndexOf("/") + 1) : raw;
  return withoutPrefix;
}

/**
 * Resolve the rate for a model. Returns null when the model has no price,
 * which the callers render as "unpriced" instead of a zero cost.
 *
 * A table entry that is explicitly null means "this model is known but has
 * no published price", which is different from "we have never heard of it".
 * Both end up unpriced, but the explicit entry stops a prefix match from
 * accidentally picking up a different model's rate.
 */
function resolvePricing(modelId, pricing) {
  const table = pricing || DEFAULT_PRICING;
  const key = pricingKey(modelId);
  if (!key) return null;
  if (key in table) return table[key] || null;
  // Fall back to a prefix match so a dated variant such as
  // "MiniMax-M3.1-Flash-Preview-20250901" still finds its base rate.
  for (const [name, rate] of Object.entries(table)) {
    if (!rate) continue;
    if (key.startsWith(name)) return rate;
  }
  return null;
}

/**
 * Cost of one request in dollars, or null when the model is unpriced.
 *
 * Cached prompt tokens are billed at the cache rate, and the remaining
 * prompt tokens at the normal rate. Reasoning tokens are part of the
 * completion and are not billed twice.
 *
 * When the model publishes a long-context tier, the part of the prompt above
 * the threshold is billed at the higher rate, and the whole completion is
 * billed at the tier's completion rate.
 */
function requestCost(modelId, promptTokens, completionTokens, cachedTokens, pricing) {
  const rate = resolvePricing(modelId, pricing);
  if (!rate) return null;
  const cached = Math.max(0, cachedTokens || 0);
  const prompt = Math.max(0, promptTokens || 0);
  const freshPrompt = Math.max(0, prompt - cached);
  const perMillion = 1_000_000;
  const cachedRate = rate.cachedPrompt != null ? rate.cachedPrompt : rate.prompt || 0;

  let promptRate = rate.prompt || 0;
  let completionRate = rate.completion || 0;
  const tier = rate.longContext;
  if (tier && prompt > tier.overTokens) {
    promptRate = tier.prompt;
    completionRate = tier.completion;
  }

  const cost =
    (freshPrompt * promptRate) / perMillion +
    (cached * cachedRate) / perMillion +
    ((completionTokens || 0) * completionRate) / perMillion;
  return cost;
}

/** List session directory names under a sessions root. */
/** A session directory name is a day, e.g. "2026-10-22". */
const SESSION_DAY = /^\d{4}-\d{2}-\d{2}$/;

/**
 * Find every session directory below the sessions root.
 *
 * The tree is two levels deep for multi-user installs (a user directory
 * holding the day directories) and one level deep for a single user. A
 * directory counts as a session when it holds a usage.json, because the
 * channel folders (a voice or chat channel) keep their ledger directly in
 * the folder rather than in a day subdirectory.
 */
function listSessionDirs(sessionsRoot) {
  const found = [];
  const seen = new Set();
  const hasLedger = (dir) => {
    try {
      return fs.statSync(path.join(dir, "usage.json")).isFile();
    } catch {
      return false;
    }
  };
  const walk = (dir, depth) => {
    let entries;
    try {
      entries = fs.readdirSync(dir, { withFileTypes: true });
    } catch {
      return;
    }
    for (const entry of entries) {
      if (!entry.isDirectory()) continue;
      const full = path.join(dir, entry.name);
      if (hasLedger(full)) {
        if (!seen.has(full)) {
          seen.add(full);
          found.push({ dir: full, name: entry.name });
        }
        continue;
      }
      // One level of nesting covers the per-user directories.
      if (depth < 2) walk(full, depth + 1);
    }
  };
  walk(sessionsRoot, 0);
  return found.sort((a, b) => (a.dir < b.dir ? -1 : a.dir > b.dir ? 1 : 0));
}

/**
 * Read the model id recorded for a session. The session file is named after
 * the day, and usage.json sits next to it.
 */
function readSessionModel(sessionDir) {
  let names;
  try {
    names = fs.readdirSync(sessionDir, { withFileTypes: true });
  } catch {
    return "";
  }
  for (const entry of names) {
    if (!entry.isFile()) continue;
    if (!entry.name.endsWith(".json")) continue;
    if (entry.name === "usage.json") continue;
    const data = readJsonSafe(path.join(sessionDir, entry.name), null);
    if (data && typeof data === "object" && typeof data.model === "string") {
      return data.model;
    }
  }
  return "";
}

/**
 * Read the configured models and build a map from the upstream model name to
 * the configuration id.
 *
 * A session records the upstream model name (for example "MiniMax-M3"), while
 * the configuration id is what the owner named the entry ("minimax-m3" or
 * "user_minimax-m3-1-flash"). The configuration id is the closer thing to a
 * provider, so it is what the panel groups by.
 */
function readProviderMap(configPath) {
  const config = readJsonSafe(configPath, null);
  if (!config || !config.models || typeof config.models !== "object") return {};
  const map = {};
  for (const [id, entry] of Object.entries(config.models)) {
    const api = entry && typeof entry === "object" ? entry.api : null;
    if (!api || typeof api !== "object") continue;
    const upstream = typeof api.model === "string" ? api.model : "";
    if (!upstream) continue;
    // The first configuration wins, so a later entry cannot steal the
    // requests of an earlier one that uses the same upstream model.
    if (!map[upstream]) map[upstream] = id;
  }
  return map;
}

/**
 * Read the day a session belongs to from the session file name, e.g.
 * "2026-10-22.json". A channel folder names its file after the day too.
 */
function readSessionDay(sessionDir) {
  let names;
  try {
    names = fs.readdirSync(sessionDir, { withFileTypes: true });
  } catch {
    return "";
  }
  for (const entry of names) {
    if (!entry.isFile()) continue;
    if (!entry.name.endsWith(".json")) continue;
    if (entry.name === "usage.json") continue;
    const day = entry.name.slice(0, 10);
    if (SESSION_DAY.test(day)) return day;
  }
  return "";
}

/** The local day of a millisecond timestamp, as "YYYY-MM-DD". */
function dayFromTs(ts) {
  if (!ts) return "";
  const date = new Date(ts);
  if (Number.isNaN(date.getTime())) return "";
  const month = String(date.getMonth() + 1).padStart(2, "0");
  const day = String(date.getDate()).padStart(2, "0");
  return `${date.getFullYear()}-${month}-${day}`;
}

/** The local day a directory was last written to, as a last resort. */
function sessionDirMtimeDay(sessionDir) {
  try {
    return dayFromTs(fs.statSync(sessionDir).mtimeMs);
  } catch {
    return "";
  }
}

/**
 * Collect every request record from the sessions tree.
 *
 * Each record is one model request with the fields the statistics panel
 * needs. Records are sorted newest first by the time they can be derived
 * from the session directory name, because a turn does not carry a
 * timestamp of its own.
 */
function collectUsageRecords(sessionsRoot, options = {}) {
  const pricing = options.pricing || DEFAULT_PRICING;
  const providerMap = options.providerMap || {};
  const records = [];
  for (const session of listSessionDirs(sessionsRoot)) {
    const name = session.name;
    const sessionDir = session.dir;
    const usage = readJsonSafe(path.join(sessionDir, "usage.json"), null);
    if (!usage || !Array.isArray(usage.turns)) continue;
    const fallbackModel = readSessionModel(sessionDir);
    // The day comes from the directory name when it is one, and from the
    // session file name otherwise, because a channel folder keeps its
    // ledger in a folder that is not named after a day.
    const sessionDay = SESSION_DAY.test(name) ? name : readSessionDay(sessionDir);
    usage.turns.forEach((turn, index) => {
      if (!turn || typeof turn !== "object") return;
      const model = turn.model || fallbackModel || "";
      const promptTokens = Number(turn.prompt_tokens) || 0;
      const completionTokens = Number(turn.completion_tokens) || 0;
      const cachedTokens = Number(turn.cached_tokens) || 0;
      const reasoningTokens = Number(turn.reasoning_tokens) || 0;
      const durationMs = Number(turn.duration_ms) || 0;
      const ts = Number(turn.ts) || 0;
      const day = sessionDay || dayFromTs(ts) || sessionDirMtimeDay(sessionDir);
      const provider = turn.provider || providerMap[model] || "";
      const cost = requestCost(model, promptTokens, completionTokens, cachedTokens, pricing);
      records.push({
        id: `${name}:${index}`,
        session: name,
        // The full relative path tells a multi-user session apart from a
        // single user one, since the day directory alone is not unique.
        session_path: path.relative(sessionsRoot, sessionDir) || name,
        day,
        ts,
        step: Number(turn.step) || 0,
        message_index: Number(turn.message_index) || 0,
        model,
        provider,
        prompt_tokens: promptTokens,
        completion_tokens: completionTokens,
        cached_tokens: cachedTokens,
        reasoning_tokens: reasoningTokens,
        // The visible output excludes the reasoning tokens, which is what the
        // panel shows as "real" consumption.
        real_tokens: Math.max(0, promptTokens + completionTokens - cachedTokens),
        duration_ms: durationMs,
        cost,
        priced: cost != null,
        tools_used: turn.tools_used || {},
      });
    });
  }
  return records;
}

/** Day bucket key for a record, matching the panel's day grouping. */
function dayKey(record) {
  return record.day || "";
}

/**
 * Hour bucket key for a record.
 *
 * The turn timestamp is only present for requests recorded after the usage
 * ledger started carrying it. Older turns fall back to the day bucket so
 * they still show up in the chart.
 */
function hourKey(record) {
  if (!record.ts) return "";
  const date = new Date(record.ts);
  if (Number.isNaN(date.getTime())) return "";
  // Both the day and the hour come from the local time, so a record is not
  // filed under a UTC day and a local hour at the same time.
  const day = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(
    date.getDate()
  ).padStart(2, "0")}`;
  const hour = String(date.getHours()).padStart(2, "0");
  return `${day} ${hour}:00`;
}

/**
 * Aggregate the records into the summary the panel header shows.
 */
function summarizeUsage(records) {
  let promptTokens = 0;
  let completionTokens = 0;
  let cachedTokens = 0;
  let reasoningTokens = 0;
  let cost = 0;
  let pricedCount = 0;
  let unpricedCount = 0;

  for (const record of records) {
    promptTokens += record.prompt_tokens;
    completionTokens += record.completion_tokens;
    cachedTokens += record.cached_tokens;
    reasoningTokens += record.reasoning_tokens;
    if (record.cost != null) {
      cost += record.cost;
      pricedCount++;
    } else {
      unpricedCount++;
    }
  }

  const totalTokens = promptTokens + completionTokens;
  const cacheHitRate = promptTokens > 0 ? cachedTokens / promptTokens : 0;

  return {
    total_requests: records.length,
    prompt_tokens: promptTokens,
    completion_tokens: completionTokens,
    cached_tokens: cachedTokens,
    reasoning_tokens: reasoningTokens,
    total_tokens: totalTokens,
    // The panel shows the tokens that were really consumed, i.e. the prompt
    // that was not served from cache plus the produced output.
    real_tokens: Math.max(0, totalTokens - cachedTokens),
    cache_hit_rate: cacheHitRate,
    total_cost: cost,
    priced_requests: pricedCount,
    unpriced_requests: unpricedCount,
  };
}

/**
 * Build the trend series, bucketed by day or hour.
 */
function buildTrend(records, granularity = "day") {
  const buckets = new Map();
  const keyOf = granularity === "hour" ? hourKey : dayKey;
  for (const record of records) {
    const key = keyOf(record);
    if (!key) continue;
    let bucket = buckets.get(key);
    if (!bucket) {
      bucket = {
        key,
        requests: 0,
        prompt_tokens: 0,
        completion_tokens: 0,
        cached_tokens: 0,
        real_tokens: 0,
        cost: 0,
        priced: false,
      };
      buckets.set(key, bucket);
    }
    bucket.requests++;
    bucket.prompt_tokens += record.prompt_tokens;
    bucket.completion_tokens += record.completion_tokens;
    bucket.cached_tokens += record.cached_tokens;
    bucket.real_tokens += record.real_tokens;
    if (record.cost != null) {
      bucket.cost += record.cost;
      bucket.priced = true;
    }
  }
  return Array.from(buckets.values()).sort((a, b) => (a.key < b.key ? -1 : a.key > b.key ? 1 : 0));
}

/**
 * Group the records by provider and model, for the filter dropdowns.
 */
function buildDimensions(records) {
  const providers = new Map();
  const models = new Map();
  for (const record of records) {
    const provider = record.provider || "";
    const model = record.model || "";
    if (provider) providers.set(provider, (providers.get(provider) || 0) + 1);
    if (model) models.set(model, (models.get(model) || 0) + 1);
  }
  return {
    providers: Array.from(providers.keys()).sort(),
    models: Array.from(models.keys()).sort(),
  };
}

/**
 * Apply the panel filters: provider, model, session and time range.
 */
function filterRecords(records, query = {}) {
  const provider = String(query.provider || "").trim();
  const model = String(query.model || "").trim();
  const session = String(query.session || "").trim();
  const from = String(query.from || "").trim();
  const to = String(query.to || "").trim();
  return records.filter((record) => {
    if (provider && record.provider !== provider) return false;
    if (model && record.model !== model) return false;
    if (session && record.session !== session) return false;
    if (from && record.day && record.day < from) return false;
    if (to && record.day && record.day > to) return false;
    return true;
  });
}

/**
 * The full statistics payload the panel consumes.
 */
function buildUsageStats(sessionsRoot, query = {}, options = {}) {
  const all = collectUsageRecords(sessionsRoot, options);
  const records = filterRecords(all, query);
  const limit = Math.max(1, Math.min(1000, Number(query.limit) || 200));
  const offset = Math.max(0, Number(query.offset) || 0);
  const granularity = query.granularity === "hour" ? "hour" : "day";

  const trend = buildTrend(records, granularity);
  // Turns recorded before the ledger carried a timestamp cannot be placed
  // in an hour bucket. Rather than showing an empty chart, fall back to the
  // day view and say so, so the panel never looks broken.
  let effectiveGranularity = granularity;
  if (granularity === "hour" && trend.length === 0) {
    effectiveGranularity = "day";
    trend.length = 0;
    trend.push(...buildTrend(records, "day"));
  }

  return {
    summary: summarizeUsage(records),
    total_summary: summarizeUsage(all),
    trend,
    granularity: effectiveGranularity,
    dimensions: buildDimensions(all),
    records: records.slice(offset, offset + limit),
    pagination: {
      total: records.length,
      limit,
      offset,
      has_more: offset + limit < records.length,
    },
  };
}

module.exports = {
  DEFAULT_PRICING,
  collectUsageRecords,
  summarizeUsage,
  buildTrend,
  buildDimensions,
  filterRecords,
  buildUsageStats,
  readProviderMap,
  requestCost,
  resolvePricing,
  pricingKey,
};
