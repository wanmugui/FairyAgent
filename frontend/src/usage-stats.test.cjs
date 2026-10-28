const test = require("node:test");
const assert = require("node:assert");
const fs = require("fs");
const os = require("os");
const path = require("path");

const usageStats = require("./usage-stats.cjs");

/** Build a fake sessions tree with the given turns per session. */
function makeSessions(specs) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "usage-stats-"));
  for (const [name, turns] of Object.entries(specs)) {
    const dir = path.join(root, name);
    fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(
      path.join(dir, "usage.json"),
      JSON.stringify({ prompt_tokens: 0, completion_tokens: 0, turns })
    );
  }
  return root;
}

test("requestCost prices a request with the plain rate", () => {
  const pricing = { "m": { prompt: 1, completion: 2, cachedPrompt: 0.1 } };
  const cost = usageStats.requestCost("m", 1_000_000, 1_000_000, 0, pricing);
  assert.strictEqual(cost, 3);
});

test("requestCost bills cached prompt tokens at the cache rate", () => {
  const pricing = { "m": { prompt: 1, completion: 2, cachedPrompt: 0.1 } };
  // 500k fresh at $1/M plus 500k cached at $0.1/M plus 1M output at $2/M.
  const cost = usageStats.requestCost("m", 1_000_000, 1_000_000, 500_000, pricing);
  assert.strictEqual(cost, 2.55);
});

test("requestCost applies the long context tier above the threshold", () => {
  const pricing = {
    "m": {
      prompt: 0.6,
      completion: 4.6,
      cachedPrompt: 0.6,
      longContext: { overTokens: 512_000, prompt: 1.6, completion: 6.9 },
    },
  };
  // Below the threshold the plain rate applies.
  assert.strictEqual(usageStats.requestCost("m", 512_000, 0, 0, pricing), 0.3072);
  // Above it, the whole prompt and completion move to the tier rate.
  assert.strictEqual(usageStats.requestCost("m", 513_000, 0, 0, pricing), 0.8208);
});

test("requestCost returns null for an unpriced model", () => {
  assert.strictEqual(usageStats.requestCost("unknown-model", 1000, 1000, 0, {}), null);
});

test("an explicitly unpriced model is not filled in by a prefix match", () => {
  const pricing = { "m3.1": { prompt: 5, completion: 5 }, "m3.1-preview": null };
  assert.strictEqual(usageStats.resolvePricing("m3.1-preview", pricing), null);
  assert.deepStrictEqual(usageStats.resolvePricing("m3.1-20250901", pricing), {
    prompt: 5,
    completion: 5,
  });
});

test("summarizeUsage reports the real tokens and the cache hit rate", () => {
  const records = [
    { prompt_tokens: 800, completion_tokens: 200, cached_tokens: 400, reasoning_tokens: 0, cost: 1 },
    { prompt_tokens: 200, completion_tokens: 100, cached_tokens: 0, reasoning_tokens: 0, cost: null },
  ];
  const summary = usageStats.summarizeUsage(records);
  assert.strictEqual(summary.total_requests, 2);
  assert.strictEqual(summary.prompt_tokens, 1000);
  assert.strictEqual(summary.completion_tokens, 300);
  assert.strictEqual(summary.cached_tokens, 400);
  assert.strictEqual(summary.total_tokens, 1300);
  assert.strictEqual(summary.real_tokens, 900);
  assert.strictEqual(summary.cache_hit_rate, 0.4);
  assert.strictEqual(summary.total_cost, 1);
  assert.strictEqual(summary.priced_requests, 1);
  assert.strictEqual(summary.unpriced_requests, 1);
});

test("collectUsageRecords reads the turns and backfills the model", () => {
  const root = makeSessions({
    "2026-10-22": [
      { step: 1, prompt_tokens: 100, completion_tokens: 10, duration_ms: 500, model: "M1" },
      { step: 2, prompt_tokens: 200, completion_tokens: 20, duration_ms: 600 },
    ],
  });
  fs.writeFileSync(
    path.join(root, "2026-10-22", "2026-10-22.json"),
    JSON.stringify({ model: "M1" })
  );
  const records = usageStats.collectUsageRecords(root, { providerMap: { M1: "p1" } });
  assert.strictEqual(records.length, 2);
  assert.strictEqual(records[0].model, "M1");
  // The second turn carries no model, so it inherits the session model.
  assert.strictEqual(records[1].model, "M1");
  assert.strictEqual(records[0].provider, "p1");
  assert.strictEqual(records[0].day, "2026-10-22");
});

test("filterRecords applies the provider, model and day filters", () => {
  const records = [
    { provider: "a", model: "m1", day: "2026-10-01" },
    { provider: "b", model: "m2", day: "2026-10-05" },
    { provider: "a", model: "m2", day: "2026-10-09" },
  ];
  assert.strictEqual(usageStats.filterRecords(records, { provider: "a" }).length, 2);
  assert.strictEqual(usageStats.filterRecords(records, { model: "m2" }).length, 2);
  assert.strictEqual(usageStats.filterRecords(records, { from: "2026-10-02" }).length, 2);
  assert.strictEqual(usageStats.filterRecords(records, { to: "2026-10-05" }).length, 2);
  assert.strictEqual(usageStats.filterRecords(records, {}).length, 3);
});

test("a channel folder with its own ledger is collected as a session", () => {
  // A voice or chat channel keeps usage.json directly in its folder, and
  // the folder is not named after a day. Those requests are real and must
  // not be dropped.
  const root = makeSessions({
    "2026-10-22": [{ step: 1, prompt_tokens: 10, completion_tokens: 1, model: "M1" }],
    "voice-private-user1": [
      { step: 1, prompt_tokens: 20, completion_tokens: 2, model: "M1" },
    ],
  });
  fs.writeFileSync(
    path.join(root, "voice-private-user1", "2026-10-21.json"),
    JSON.stringify({ model: "M1" })
  );
  const records = usageStats.collectUsageRecords(root, {});
  assert.strictEqual(records.length, 2);
  const channel = records.find((r) => r.session === "voice-private-user1");
  assert.ok(channel, "the channel folder must be collected");
  // The day comes from the session file name, not from the folder name.
  assert.strictEqual(channel.day, "2026-10-21");
});

test("a folder without a ledger is not treated as a session", () => {
  const root = makeSessions({
    "2026-10-22": [{ step: 1, prompt_tokens: 10, completion_tokens: 1, model: "M1" }],
  });
  fs.mkdirSync(path.join(root, "not-a-session"), { recursive: true });
  const records = usageStats.collectUsageRecords(root, {});
  assert.strictEqual(records.length, 1);
  assert.strictEqual(records[0].session, "2026-10-22");
});

test("buildUsageStats paginates the records", () => {
  const turns = Array.from({ length: 10 }, (_, i) => ({
    step: i,
    prompt_tokens: 100,
    completion_tokens: 10,
    model: "M1",
  }));
  const root = makeSessions({ "2026-10-22": turns });
  const stats = usageStats.buildUsageStats(root, { limit: 3, offset: 0 });
  assert.strictEqual(stats.records.length, 3);
  assert.strictEqual(stats.pagination.total, 10);
  assert.strictEqual(stats.pagination.has_more, true);
  const second = usageStats.buildUsageStats(root, { limit: 3, offset: 9 });
  assert.strictEqual(second.records.length, 1);
  assert.strictEqual(second.pagination.has_more, false);
});

test("buildTrend groups by day and by hour", () => {
  const records = [
    { day: "2026-10-01", ts: Date.parse("2026-10-01T10:30:00Z"), requests: 1 },
    { day: "2026-10-01", ts: Date.parse("2026-10-01T10:45:00Z"), requests: 1 },
    { day: "2026-10-02", ts: Date.parse("2026-10-02T11:00:00Z"), requests: 1 },
  ];
  const byDay = usageStats.buildTrend(records, "day");
  assert.strictEqual(byDay.length, 2);
  assert.strictEqual(byDay[0].requests, 2);
  const byHour = usageStats.buildTrend(records, "hour");
  // The two records 15 minutes apart share an hour bucket, the third one is
  // a day later, so two buckets.
  assert.strictEqual(byHour.length, 2);
  assert.strictEqual(byHour[0].requests, 2);
});

test("the hour bucket uses one timezone for both the day and the hour", () => {
  // 23:30 local belongs to the same day as the local hour, whatever the
  // UTC offset is. Mixing toISOString for the day and getHours for the hour
  // would file this under the next day.
  const ts = new Date(2026, 9, 1, 23, 30, 0).getTime();
  const bucket = usageStats.buildTrend([{ day: "2026-10-01", ts, requests: 1 }], "hour")[0];
  assert.ok(bucket.key.startsWith("2026-10-01 "), `unexpected bucket: ${bucket.key}`);
  assert.strictEqual(bucket.key, "2026-10-01 23:00");
});

test("the hour view falls back to the day view without timestamps", () => {
  const records = [{ day: "2026-10-01", ts: 0, requests: 1 }];
  const stats = usageStats.buildUsageStats(
    makeSessions({ "2026-10-01": [{ step: 1, prompt_tokens: 1, completion_tokens: 1 }] }),
    { granularity: "hour" }
  );
  // The fake turn has no timestamp, so the panel reports the day grain.
  assert.strictEqual(stats.granularity, "day");
  assert.strictEqual(stats.trend.length, 1);
  assert.ok(records.length === 1);
});
