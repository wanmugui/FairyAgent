import React, { useCallback, useEffect, useMemo, useState } from "react";
import {
  Building2,
  ChevronDown,
  Eye,
  EyeOff,
  Loader2,
  Plus,
  RefreshCw,
  Save,
  Trash2,
} from "lucide-react";
import {
  commitProviderModels,
  createProvider,
  fetchProviders,
  listProviderAvailableModels,
  removeProvider,
  saveProvider,
} from "../api/chat";

/**
 * Provider management.
 *
 * A provider is the unit a credential belongs to: one base url, one key, one
 * upstream dialect. The tab strip on top lists the providers with their
 * model counts, the model row shows the models that provider serves, and the
 * form below edits the provider itself.
 *
 * The API key comes back masked. The reveal button asks the backend for the
 * plaintext once, because a masked value cannot be checked against the
 * provider's own dashboard.
 */

const UPSTREAM_FORMATS = [
  { id: "chat_completions", label: "OpenAI Chat Completions" },
  { id: "responses", label: "OpenAI Responses" },
  { id: "anthropic_messages", label: "Anthropic Messages（Claude）" },
];

const OUTPUT_LIMIT_FIELDS = [
  { id: "max_tokens", label: "max_tokens（Chat Completions / Claude）" },
  { id: "max_output_tokens", label: "max_output_tokens（Responses）" },
];

/** The output limit field each upstream dialect expects. */
function defaultOutputLimitField(format) {
  return format === "responses" ? "max_output_tokens" : "max_tokens";
}

const EMPTY_DRAFT = {
  id: "",
  name: "",
  note: "",
  homepage: "",
  base_url: "",
  format: "chat_completions",
  output_limit_field: "max_tokens",
  output_limit: 16384,
  api_key: "",
};

/** A short label for the model button, keeping the provider prefix. */
function modelLabel(model) {
  const id = model.id || "";
  const display = model.display || id;
  return id && !display.includes(id) ? `${id}（${display}）` : display;
}

export default function ProviderSection({ models = [], onModelsChanged }) {
  const [providers, setProviders] = useState([]);
  const [activeId, setActiveId] = useState("");
  const [draft, setDraft] = useState(EMPTY_DRAFT);
  const [revealed, setRevealed] = useState(false);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [availableModels, setAvailableModels] = useState([]);
  const [showAllModels, setShowAllModels] = useState(false);
  const [committing, setCommitting] = useState(false);

  const load = useCallback(async (options = {}) => {
    setLoading(true);
    setError("");
    try {
      const data = await fetchProviders(options);
      if (data && data.error) throw new Error(data.error);
      const list = Array.isArray(data?.providers) ? data.providers : [];
      setProviders(list);
      return list;
    } catch (err) {
      setError(err.message || String(err));
      return [];
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load().then((list) => {
      // Open on the first provider, so the panel shows a filled form
      // instead of an empty one.
      if (list.length && !activeId) {
        selectProvider(list[0].id);
      }
    });
    // selectProvider is stable enough for this effect: it only depends on
    // load, which is a useCallback with no dependencies.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [load]);

  // With no provider selected the panel shows every model, so the "all
  // models" tab is not an empty view.
  const providerModels = useMemo(() => {
    if (!activeId) return models;
    return models.filter((model) => model.provider === activeId);
  }, [models, activeId]);

  const totalModels = models.length;

  // The model ids this provider already serves. A model that is already
  // there is marked as in use instead of being offered again.
  //
  // The comparison is case insensitive and looks at the api model as well,
  // because the upstream list names a model "MiniMax-M3" while the
  // configuration stores it under the id "minimax-m3".
  const activeModelIds = useMemo(() => {
    const ids = new Set();
    const add = (value) => {
      const text = String(value || "").trim().toLowerCase();
      if (text) ids.add(text);
    };
    const provider = providers.find((p) => p.id === activeId);
    if (provider && Array.isArray(provider.models)) provider.models.forEach(add);
    providerModels.forEach((m) => {
      add(m.id);
      add(m.apiModel);
      add(m.api_model);
    });
    return ids;
  }, [providers, activeId, providerModels]);

  // Select a provider and load its form values. The key is shown masked by
  // default; the eye button asks the backend for the plaintext once, because
  // a masked value cannot be checked against the provider's own dashboard.
  const selectProvider = useCallback(
    async (id) => {
      setActiveId(id);
      setMessage("");
      setError("");
      setAvailableModels([]);
      setShowAllModels(false);
      setRevealed(false);
      if (!id) {
        setDraft(EMPTY_DRAFT);
        return;
      }
      const list = await load();
      const found = list.find((p) => p.id === id);
      if (found) {
        setDraft({
          id: found.id,
          name: found.name || "",
          note: found.note || "",
          homepage: found.homepage || "",
          base_url: found.base_url || "",
          format: found.format || "chat_completions",
          output_limit_field: found.output_limit_field || "max_tokens",
          output_limit: found.output_limit || 0,
          api_key: found.api_key_masked || "",
        });
      }
    },
    [load]
  );

  const update = (patch) => setDraft((prev) => ({ ...prev, ...patch }));

  const handleSave = async () => {
    setSaving(true);
    setError("");
    setMessage("");
    try {
      const payload = {
        name: draft.name,
        note: draft.note,
        homepage: draft.homepage,
        base_url: draft.base_url,
        format: draft.format,
        output_limit_field: draft.output_limit_field,
        output_limit: Number(draft.output_limit) || 0,
      };
      // A masked key is not a new key. Sending it would replace the stored
      // one with the mask, so it is left out and the backend keeps what it
      // has. An empty field means the same thing.
      if (draft.api_key && revealed) payload.api_key = draft.api_key;
      const id = draft.id || draft.name || draft.base_url;
      const result = draft.id
        ? await saveProvider(draft.id, payload)
        : await createProvider({ ...payload, id });
      if (!result || result.ok === false) {
        throw new Error(result?.error || "保存失败");
      }
      setMessage("已保存");
      await load();
      if (result.provider) await selectProvider(result.provider);
    } catch (err) {
      setError(err.message || String(err));
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async () => {
    if (!draft.id) return;
    setBusy(true);
    setError("");
    setMessage("");
    try {
      const result = await removeProvider(draft.id);
      if (!result || result.ok === false) throw new Error(result?.error || "删除失败");
      setActiveId("");
      setDraft(EMPTY_DRAFT);
      setMessage("已删除");
      await load();
    } catch (err) {
      setError(err.message || String(err));
    } finally {
      setBusy(false);
    }
  };

  const handleFetchModels = async () => {
    if (!draft.id) {
      setError("请先保存供应商，再拉取可用模型");
      return;
    }
    setBusy(true);
    setError("");
    setMessage("");
    try {
      const result = await listProviderAvailableModels(draft.id);
      if (!result || result.ok === false) throw new Error(result?.error || "拉取失败");
      const list = Array.isArray(result.models) ? result.models : [];
      setAvailableModels(list);
      setMessage(list.length ? `拉到 ${list.length} 个可用模型` : "该服务没有返回模型列表");
    } catch (err) {
      setError(err.message || String(err));
    } finally {
      setBusy(false);
    }
  };

  // Commit one or more pulled model ids to the provider. The backend merges
  // them into provider.models, derives config.models from it and answers with
  // the final model list.
  const commitModels = async (names) => {
    const list = Array.isArray(names) ? names.filter(Boolean).map(String) : [];
    if (!draft.id) {
      setError("请先保存供应商，再添加模型");
      return;
    }
    if (!list.length) {
      setError("没有需要添加的模型");
      return;
    }
    setCommitting(true);
    setError("");
    setMessage("");
    try {
      const result = await commitProviderModels(draft.id, list);
      if (!result || result.ok === false) {
        throw new Error(result?.error || "添加模型失败");
      }
      // The backend answers a commit with committed:true and the list of
      // models it actually added. Without that flag the models were only
      // listed, not written, so the panel says so instead of pretending.
      if (result.committed !== true) {
        setError(
          "后端尚未实现模型落地接口（commit），模型没有写入配置；接口就绪后重试即可。"
        );
        return;
      }
      const added = Array.isArray(result.added) ? result.added : list;
      setMessage(
        added.length ? `已添加 ${added.length} 个模型：${added.join("、")}` : "模型已添加"
      );
      await load();
      if (onModelsChanged) onModelsChanged();
    } catch (err) {
      setError(err.message || String(err));
    } finally {
      setCommitting(false);
    }
  };

  const handleAddModel = (name) => commitModels([name]);

  const handleAddAllModels = () => {
    const pending = availableModels.filter(
      (name) => !activeModelIds.has(String(name).trim().toLowerCase())
    );
    return commitModels(pending);
  };

  const handleToggleReveal = async () => {    if (revealed) {
      // Hide it again and put the masked value back, so the field never
      // keeps the plaintext around after the owner looks at it.
      setRevealed(false);
      const list = await load();
      const found = list.find((p) => p.id === draft.id);
      setDraft((prev) => ({ ...prev, api_key: found?.api_key_masked || "" }));
      return;
    }
    const list = await load({ reveal: true });
    const found = list.find((p) => p.id === draft.id);
    if (found) {
      setDraft((prev) => ({ ...prev, api_key: found.api_key || "" }));
      setRevealed(true);
    }
  };

  const shownModels = showAllModels ? models : providerModels;
  return (
    <div className="provider-panel">
      {/* Tab strip: every provider with its model count, plus the way in. */}
      <div className="provider-tabs">
        <button
          type="button"
          className={`provider-tab${activeId === "" ? " is-active" : ""}`}
          onClick={() => selectProvider("")}
        >
          全部模型 <span className="provider-tab-count">{totalModels}</span>
        </button>
        {providers.map((provider) => (
          <button
            key={provider.id}
            type="button"
            className={`provider-tab${activeId === provider.id ? " is-active" : ""}`}
            onClick={() => selectProvider(provider.id)}
          >
            {provider.name || provider.id}
            <span className="provider-tab-count">{provider.model_count}</span>
          </button>
        ))}
        <button
          type="button"
          className="provider-tab provider-tab-add"
          onClick={() => {
            setActiveId("");
            setDraft({ ...EMPTY_DRAFT, id: `provider_${Date.now()}` });
            setRevealed(true);
            setMessage("");
            setError("");
          }}
        >
          <Plus size={13} /> 新增供应商
        </button>
        <button
          type="button"
          className="provider-tab provider-tab-refresh"
          onClick={() => load()}
          title="刷新供应商列表"
        >
          <RefreshCw size={13} className={loading ? "provider-spin" : ""} />
        </button>
      </div>

      {/* The models of the selected provider. */}
      <div className="provider-models">
        <div className="provider-models-head">
          <span>
            {activeId
              ? `「${providers.find((p) => p.id === activeId)?.name || activeId}」的模型 · ${providerModels.length}`
              : `全部模型 · ${totalModels}`}
          </span>
          {activeId ? (
            <button type="button" className="provider-link" onClick={() => setShowAllModels((v) => !v)}>
              {showAllModels ? "只看本供应商" : "显示全部模型"}
            </button>
          ) : null}
        </div>
        {shownModels.length ? (
          <div className="provider-model-chips">
            {shownModels.map((model) => (
              <button key={model.id} type="button" className="provider-model-chip">
                {modelLabel(model)}
              </button>
            ))}
          </div>
        ) : (
          <div className="provider-empty">
            {activeId ? "该供应商下还没有模型" : "还没有模型"}
          </div>
        )}
      </div>

      {/* The provider form. */}
      <div className="provider-form">
        <div className="provider-form-title">
          <Building2 size={14} />
          <span>{draft.id ? `编辑供应商 · ${draft.id}` : "新增供应商"}</span>
        </div>

        <div className="fairy-form">
          <label className="fairy-field">
            <span className="fairy-field-label">名称</span>
            <input
              className="fairy-input"
              value={draft.name}
              placeholder="例如 MiniMax"
              onChange={(e) => update({ name: e.target.value })}
            />
          </label>

          <label className="fairy-field">
            <span className="fairy-field-label">备注</span>
            <input
              className="fairy-input"
              value={draft.note}
              placeholder="例如 官方"
              onChange={(e) => update({ note: e.target.value })}
            />
          </label>

          <label className="fairy-field">
            <span className="fairy-field-label">官网链接</span>
            <input
              className="fairy-input"
              value={draft.homepage}
              placeholder="https://"
              onChange={(e) => update({ homepage: e.target.value })}
            />
          </label>

          <label className="fairy-field">
            <span className="fairy-field-label">API Key</span>
            <div className="provider-key-row">
              <input
                className="fairy-input"
                type={revealed ? "text" : "password"}
                value={draft.api_key}
                placeholder="未设置"
                onChange={(e) => update({ api_key: e.target.value })}
              />
              <button
                type="button"
                className="provider-icon-btn"
                onClick={handleToggleReveal}
                title={revealed ? "隐藏" : "显示"}
              >
                {revealed ? <EyeOff size={14} /> : <Eye size={14} />}
              </button>
            </div>
          </label>

          <label className="fairy-field">
            <span className="fairy-field-label">API 请求地址</span>
            <input
              className="fairy-input"
              value={draft.base_url}
              placeholder="https://api.example.com/v1"
              onChange={(e) => update({ base_url: e.target.value })}
            />
          </label>

          <div className="fairy-field">
            <span className="fairy-field-label">接口说明</span>
            <div className="provider-hint">
              兼容 OpenAI Chat Completions 的服务填到 <code>/v1</code>；带 <code>/responses</code> 的地址
              请把上游格式选为 responses。
            </div>
          </div>

          <label className="fairy-field">
            <span className="fairy-field-label">上游格式</span>
            <div className="provider-select-row">
              <select
                className="fairy-input"
                value={draft.format}
                onChange={(e) => {
                  const format = e.target.value;
                  update({
                    format,
                    output_limit_field: defaultOutputLimitField(format),
                  });
                }}
              >
                {UPSTREAM_FORMATS.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.label}
                  </option>
                ))}
              </select>
              <ChevronDown size={13} className="provider-select-caret" />
            </div>
          </label>

          <label className="fairy-field">
            <span className="fairy-field-label">输出上限字段</span>
            <div className="provider-select-row">
              <select
                className="fairy-input"
                value={draft.output_limit_field}
                onChange={(e) => update({ output_limit_field: e.target.value })}
              >
                {OUTPUT_LIMIT_FIELDS.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.label}
                  </option>
                ))}
              </select>
              <ChevronDown size={13} className="provider-select-caret" />
            </div>
          </label>
        </div>

        {error ? <div className="provider-message is-error">{error}</div> : null}
        {message ? <div className="provider-message">{message}</div> : null}

        {availableModels.length ? (
          <div className="provider-available">
            <div className="provider-available-title">
              该服务下的可用模型（{availableModels.length}）
            </div>
            <div className="provider-available-actions">
              <button
                type="button"
                className="provider-btn"
                onClick={handleAddAllModels}
                disabled={committing}
              >
                {committing ? (
                  <Loader2 size={13} className="provider-spin" />
                ) : (
                  <Plus size={13} />
                )}
                全部添加
              </button>
            </div>
            <div className="provider-available-list">
              {availableModels.map((name) => {
                const inUse = activeModelIds.has(String(name).trim().toLowerCase());
                return (
                  <div key={name} className="provider-available-item">
                    <span className="provider-available-name">{name}</span>
                    {inUse ? (
                      <span className="provider-available-tag">已在用</span>
                    ) : (
                      /* A quiet outline button: one solid accent button per
                         row turns the list into a wall of colour. */
                      <button
                        type="button"
                        className="provider-btn provider-btn--add"
                        onClick={() => handleAddModel(name)}
                        disabled={committing}
                      >
                        {committing ? (
                          <Loader2 size={13} className="provider-spin" />
                        ) : (
                          <Plus size={13} />
                        )}
                        添加
                      </button>
                    )}
                  </div>
                );
              })}
            </div>
          </div>
        ) : null}

        <div className="provider-actions">
          <button
            type="button"
            className="provider-btn is-primary"
            onClick={handleSave}
            disabled={saving}
          >
            {saving ? <Loader2 size={13} className="provider-spin" /> : <Save size={13} />}
            保存
          </button>
          <button
            type="button"
            className="provider-btn"
            onClick={handleFetchModels}
            disabled={busy}
          >
            {busy ? <Loader2 size={13} className="provider-spin" /> : <RefreshCw size={13} />}
            拉取可用模型
          </button>
          {draft.id && providers.some((p) => p.id === draft.id) ? (
            <button
              type="button"
              className="provider-btn is-danger"
              onClick={handleDelete}
              disabled={busy}
            >
              <Trash2 size={13} />
              删除
            </button>
          ) : null}
        </div>
      </div>
    </div>
  );
}
