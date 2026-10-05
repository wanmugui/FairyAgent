import React, { useMemo, useState } from 'react';
import { Globe2 } from 'lucide-react';

function parseJSON(value) {
  if (typeof value !== 'string') return value;
  try { return JSON.parse(value); } catch { return null; }
}

function formatCount(value) {
  return (Number(value) || 0).toLocaleString();
}

function formatDurationMs(value) {
  const ms = Number(value) || 0;
  if (ms >= 60000) return (ms / 60000).toFixed(1) + 'min';
  if (ms >= 1000) return (ms / 1000).toFixed(1) + 's';
  return Math.round(ms) + 'ms';
}

const DISCLOSURE_STORAGE_PREFIX = 'fairy.tool-disclosure.v2.';

function readDisclosureState(key, fallback = false) {
  if (!key || typeof window === 'undefined') return fallback;
  try {
    const value = window.sessionStorage.getItem(DISCLOSURE_STORAGE_PREFIX + key);
    if (value === '1') return true;
    if (value === '0') return false;
  } catch {}
  return fallback;
}

function writeDisclosureState(key, value) {
  if (!key || typeof window === 'undefined') return;
  try {
    window.sessionStorage.setItem(DISCLOSURE_STORAGE_PREFIX + key, value ? '1' : '0');
  } catch {}
}

function toolDisclosureKey(scopeKey, tc, index) {
  if (!scopeKey) return '';
  const callId = tc && (tc.id || tc.tool_call_id) || index;
  return `${scopeKey}:tool:${callId}`;
}

function formatJSON(value) {
  if (value === undefined || value === null || value === '') return '';
  if (typeof value === 'string') {
    const parsed = parseJSON(value);
    return parsed === null ? value : JSON.stringify(parsed, null, 2);
  }
  try { return JSON.stringify(value, null, 2); } catch { return String(value); }
}

function lineDiff(oldLines, newLines) {
  const n = oldLines.length;
  const m = newLines.length;
  const dp = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i -= 1) {
    for (let j = m - 1; j >= 0; j -= 1) {
      dp[i][j] = oldLines[i] === newLines[j]
        ? dp[i + 1][j + 1] + 1
        : Math.max(dp[i + 1][j], dp[i][j + 1]);
    }
  }
  const rows = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (oldLines[i] === newLines[j]) {
      rows.push({ type: 'same', text: oldLines[i] });
      i += 1;
      j += 1;
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      rows.push({ type: 'del', text: oldLines[i] });
      i += 1;
    } else {
      rows.push({ type: 'add', text: newLines[j] });
      j += 1;
    }
  }
  while (i < n) { rows.push({ type: 'del', text: oldLines[i] }); i += 1; }
  while (j < m) { rows.push({ type: 'add', text: newLines[j] }); j += 1; }
  return rows;
}

function resultText(result) {
  if (!result) return '';
  return typeof result.content === 'string' ? result.content : formatJSON(result.content);
}

function resultIsError(result, parsed) {
  if (!result) return false;
  if (result.error) return true;
  if (parsed && parsed.ok === false) return true;
  const text = resultText(result);
  return /"error"\s*:/.test(text) || /^error\b/i.test(text.trim());
}

function firstErrorLine(result) {
  if (!result) return '';
  if (result.error) return String(result.error).split('\n')[0];
  const parsed = parseJSON(result.content);
  if (parsed && typeof parsed.error === 'string') return parsed.error.split('\n')[0];
  return resultText(result).split('\n').find(line => line.trim()) || '';
}

function toolMeta(name) {
  const map = {
    bash: ['终端', '⌘'],
    powershell: ['PowerShell', '⌘'],
    bash_job: ['后台终端', '⌘'],
    browser: ['浏览器', '◈'],
    read_file: ['读取文件', '▤'],
    write_file: ['写入文件', '✎'],
    edit_file: ['编辑文件', '✎'],
    glob: ['查找文件', '⌕'],
    grep: ['搜索内容', '⌕'],
    web_search: ['网页搜索', '◎'],
    web_fetch: ['抓取网页', '◎'],
    image_search: ['搜索图片', '▣'],
    image_generate: ['生成图片', '✦'],
    image_vqa: ['看图回答', '◉'],
    create_subtask: ['子任务', '⤵'],
    ask_user: ['询问用户', '?'],
    plan: ['执行计划', '☷'],
    memory_search: ['检索记忆', '◈'],
    show_result: ['展示结果', '▣'],
  };
  return map[name] || [name || '工具', '⚙'];
}

function cleanInline(value) {
  return String(value || '').replace(/\s+/g, ' ').trim();
}

function planTarget(args) {
  const action = cleanInline(args && args.action);
  if (action === 'mark') {
    const id = cleanInline(args && args.id);
    const status = cleanInline(args && args.status);
    return [id, status ? `→ ${status}` : ''].filter(Boolean).join(' ');
  }
  const items = Array.isArray(args && args.items) ? args.items : [];
  const question = cleanInline(args && args.question);
  const firstAction = items
    .map(item => cleanInline(item && (item.action || item.id)))
    .find(Boolean) || '';
  const countLabel = items.length > 1 ? `${items.length} 项` : '';
  const label = question || firstAction || (items.length ? '计划已更新' : action);
  return [label, countLabel].filter(Boolean).join(' · ');
}

function toolTarget(args, name = '') {
  if (!args || typeof args !== 'object') return '';
  if (name === 'plan') return planTarget(args);
  if (args.file_path) return args.file_path;
  if (args.path) return args.path;
  if (args.pattern) return args.pattern;
  if (args.query) return args.query;
  if (args.url) return args.url;
  if (args.title) return args.title;
  if (args.command) return String(args.command).replace(/\s+/g, ' ').slice(0, 120);
  if (args.goal) return String(args.goal).replace(/\s+/g, ' ').slice(0, 120);
  return '';
}

function summarizeToolCalls(toolCalls) {
  const counts = new Map();
  for (const tc of toolCalls) {
    const name = tc && tc.function && tc.function.name || 'tool';
    counts.set(name, (counts.get(name) || 0) + 1);
  }
  return Array.from(counts, ([name, count]) => ({
    name,
    count,
  }));
}

function toolCallTitle(tc) {
  const name = tc && tc.function && tc.function.name || 'tool';
  const args = parseJSON(tc && tc.function && tc.function.arguments);
  const target = toolTarget(args, name);
  return `${toolMeta(name)[0]} (${name})${target ? ` · ${target}` : ''}`;
}

function diffStats(name, args) {
  if (!args || typeof args !== 'object') return null;
  if (name === 'write_file' && typeof args.content === 'string') {
    return { added: args.content.split('\n').length, removed: 0 };
  }
  if (name === 'edit_file' && typeof args.old_text === 'string' && typeof args.new_text === 'string') {
    const rows = lineDiff(String(args.old_text).split('\n'), String(args.new_text).split('\n'));
    return {
      added: rows.filter(row => row.type === 'add').length,
      removed: rows.filter(row => row.type === 'del').length,
    };
  }
  return null;
}

function TerminalResult({ args, result }) {
  const command = args && (args.command || args.cmd) || '';
  const output = resultText(result);
  const parsed = parseJSON(result && result.content);
  const exitCode = parsed && parsed.exit_code;
  return (
    <div className="tool-terminal">
      {command ? <div className="tool-terminal-command"><span>$</span> {String(command)}</div> : null}
      <pre className="tool-terminal-output">{output || '(无输出)'}</pre>
      {exitCode !== undefined ? <div className="tool-terminal-exit">exit {exitCode}</div> : null}
    </div>
  );
}

function ReadResult({ args, result }) {
  const text = resultText(result);
  const lines = text ? text.split('\n') : [];
  return (
    <div className="tool-read">
      {args && (args.file_path || args.path) ? <div className="tool-read-path">{args.file_path || args.path}</div> : null}
      <pre className="tool-read-body">
        {lines.slice(0, 80).map((line, index) => (
          <div className="tool-read-line" key={index}>
            <span className="tool-read-number">{index + 1}</span>
            <span>{line || ' '}</span>
          </div>
        ))}
        {lines.length > 80 ? <div className="tool-read-more">… 还有 {lines.length - 80} 行，展开“原始数据”查看完整内容</div> : null}
      </pre>
    </div>
  );
}

function DiffResult({ name, args }) {
  if (!args) return null;
  if (name === 'edit_file' && typeof args.old_text === 'string' && typeof args.new_text === 'string') {
    const rows = lineDiff(String(args.old_text).split('\n'), String(args.new_text).split('\n'));
    return (
      <div className="tool-diff">
        <div className="tool-diff-title">{args.file_path || args.path || '文件变更'}</div>
        <pre className="tool-diff-body">
          {rows.slice(0, 200).map((row, index) => (
            <div key={index} className={'tool-diff-line ' + row.type}>
              {row.type === 'add' ? '+' : row.type === 'del' ? '-' : ' '} {row.text}
            </div>
          ))}
        </pre>
      </div>
    );
  }
  if (name === 'write_file' && typeof args.content === 'string') {
    return (
      <div className="tool-diff">
        <div className="tool-diff-title">已写入 {args.file_path || args.path || '文件'}</div>
        <pre className="tool-diff-body">
          {String(args.content).split('\n').slice(0, 200).map((line, index) => (
            <div key={index} className="tool-diff-line add">+ {line}</div>
          ))}
        </pre>
      </div>
    );
  }
  return null;
}

function SearchResult({ result }) {
  const text = resultText(result);
  const rows = text.split('\n').filter(Boolean).slice(0, 120);
  return (
    <div className="tool-search">
      {rows.length === 0 ? <div className="tool-empty">没有匹配结果</div> : rows.map((row, index) => (
        <div className="tool-search-row" key={index}>{row}</div>
      ))}
    </div>
  );
}

function WebResult({ result }) {
  const parsed = parseJSON(result && result.content);
  const groups = [];
  if (parsed && Array.isArray(parsed.sources)) {
    groups.push({
      query: parsed.action && (parsed.action.query || (parsed.action.queries || []).join(' · ')) || '',
      sources: parsed.sources,
      truncated: parsed.truncated,
    });
  } else if (parsed && Array.isArray(parsed.results)) {
    for (const entry of parsed.results) {
      if (entry && Array.isArray(entry.sources)) {
        groups.push({ query: entry.query || '', sources: entry.sources, truncated: entry.truncated, error: entry.error });
      }
    }
  } else if (parsed && Array.isArray(parsed.hits)) {
    groups.push({ query: parsed.query || '', sources: parsed.hits, truncated: parsed.truncated });
  }
  if (groups.length > 0) {
    const budget = parsed && parsed.budget;
    return (
      <div className="tool-web">
        {budget ? (
          <div className="tool-web-budget">
            <span>{budget.context_size || 'dynamic'}</span>
            <span>{formatCount(budget.used_tokens || 0)} / {formatCount(budget.max_tokens || 0)} tok</span>
            {budget.source_limit ? <span>最多 {budget.source_limit} 来源</span> : null}
          </div>
        ) : null}
        {groups.map((group, groupIndex) => (
          <div className="tool-web-group" key={groupIndex}>
            {group.query ? <div className="tool-web-query">{group.query}</div> : null}
            {group.error ? <div className="tool-web-error">{group.error}</div> : null}
            {group.sources.map((source, index) => (
              <a className="tool-web-source" key={(source.url || index) + ':' + index} href={source.url} target="_blank" rel="noopener noreferrer">
                <span className="tool-web-index">[{source.index || index + 1}]</span>
                <strong>{source.title || source.url}</strong>
                {source.url ? <span className="tool-web-url">{source.url}</span> : null}
                {source.snippet ? <span className="tool-web-snippet">{source.snippet}</span> : null}
              </a>
            ))}
            {group.truncated ? <div className="tool-web-footnote">来源或片段受动态预算裁剪</div> : null}
          </div>
        ))}
      </div>
    );
  }
  return <pre className="tool-json">{resultText(result) || '(无内容)'}</pre>;
}

function browserArtifact(value) {
  if (!value || typeof value !== 'object') return null;
  const artifact = value.artifact || (value.result && value.result.artifact);
  const pageUrl = artifact && (artifact.page_url || (artifact.kind === 'browser-live' ? artifact.live_url : ''));
  return pageUrl ? { ...artifact, kind: 'browser-live', page_url: pageUrl } : null;
}

function BrowserResult({ artifact, onOpenFile }) {
  if (!artifact) return null;
  return (
    <div className="tool-browser-view">
      <div className="tool-browser-view-bar">
        <span>{artifact.name || '浏览器页面'}</span>
        {onOpenFile ? (
          <button type="button" onClick={() => onOpenFile(artifact)}>
            在右侧打开
          </button>
        ) : null}
      </div>
      <button type="button" className="tool-browser-frame tool-browser-page-link" onClick={() => onOpenFile && onOpenFile(artifact)}>
        <Globe2 size={15} aria-hidden="true" />
        <span>{artifact.page_url}</span>
      </button>
    </div>
  );
}

function ResultView({ name, args, result, artifact = null, onOpenFile = null }) {
  if (!result) return <div className="tool-empty">执行中，等待工具返回…</div>;
  if (name === 'browser' && artifact) return <BrowserResult artifact={artifact} onOpenFile={onOpenFile} />;
  if (name === 'bash' || name === 'powershell' || name === 'bash_job') return <TerminalResult args={args} result={result} />;
  if (name === 'read_file') return <ReadResult args={args} result={result} />;
  if (name === 'edit_file' || name === 'write_file') return <DiffResult name={name} args={args} />;
  if (name === 'grep' || name === 'glob') return <SearchResult result={result} />;
  if (name === 'web_search' || name === 'web_fetch') return <WebResult result={result} />;
  return <pre className="tool-json">{resultText(result) || '(无内容)'}</pre>;
}

function ToolDetailSection({ id, label, meta = '', stateKey = '', defaultOpen = false, children }) {
  const sectionKey = stateKey ? `${stateKey}:section:${id}` : '';
  const [open, setOpen] = useState(() => readDisclosureState(sectionKey, defaultOpen));
  const toggle = () => setOpen(value => {
    const next = !value;
    writeDisclosureState(sectionKey, next);
    return next;
  });
  return (
    <section className={'tool-nested-section' + (open ? ' open' : '')}>
      <button
        type="button"
        className="tool-nested-summary"
        aria-expanded={open}
        onClick={event => {
          event.stopPropagation();
          toggle();
        }}
      >
        <span className="tool-nested-chevron">{open ? '⌄' : '›'}</span>
        <span className="tool-nested-label">{label}</span>
        {meta ? <span className="tool-nested-meta">{meta}</span> : null}
      </button>
      {open ? <div className="tool-nested-body">{children}</div> : null}
    </section>
  );
}

export default function ToolActivity({ tc, result, onOpenSubtask = null, onOpenFile = null, stateKey = '' }) {
  const name = tc && tc.function && tc.function.name || 'tool';
  const rawArgs = tc && tc.function && tc.function.arguments || '';
  const args = useMemo(() => parseJSON(rawArgs), [rawArgs]);
  const parsedResult = useMemo(() => parseJSON(result && result.content), [result && result.content]);
  const browserResultArtifact = useMemo(() => name === 'browser' ? browserArtifact(parsedResult) : null, [name, parsedResult]);
  const [open, setOpen] = useState(() => readDisclosureState(stateKey, false));
  const [subtaskJumpState, setSubtaskJumpState] = useState('');
  const [title, icon] = toolMeta(name);
  const target = toolTarget(args, name);
  const stats = diffStats(name, args);
  const failed = resultIsError(result, parsedResult);
  const running = !result;
  const summary = failed ? firstErrorLine(result) : target;
  const phaseLabel = running ? '执行中' : failed ? '失败' : '已完成';
  const toggleOpen = () => setOpen(value => {
    const next = !value;
    writeDisclosureState(stateKey, next);
    return next;
  });

  const openSubtask = async event => {
    event.stopPropagation();
    if (!onOpenSubtask || subtaskJumpState === 'loading') return;
    setSubtaskJumpState('loading');
    const opened = await onOpenSubtask(tc, result);
    if (!opened) {
      setSubtaskJumpState('missing');
      window.setTimeout(() => setSubtaskJumpState(''), 1800);
    }
  };

  return (
    <div className={'tool-activity ' + (running ? 'running' : failed ? 'error' : 'done')} data-tool={name}>
      <div
        className="tool-activity-heading"
        role="button"
        tabIndex={0}
        aria-expanded={open}
        onClick={toggleOpen}
        onKeyDown={event => {
          if (event.key === 'Enter' || event.key === ' ') {
            event.preventDefault();
            toggleOpen();
          }
        }}
      >
        <span className="tool-activity-glyph">{failed ? '●' : running ? '◌' : icon}</span>
        <span className="tool-activity-copy">
          <span className="tool-activity-title">{title}</span>
          {summary ? <span className={'tool-activity-target' + (failed ? ' error' : '')} title={summary}>{summary}</span> : null}
        </span>
        <span className="tool-activity-heading-state">
          {browserResultArtifact && onOpenFile ? (
            <button
              type="button"
              className="tool-activity-jump"
              onClick={event => {
                event.stopPropagation();
                onOpenFile(browserResultArtifact);
              }}
            >
              {browserResultArtifact.live ? '查看页面' : '查看截图'}
            </button>
          ) : null}
          {name === 'create_subtask' && onOpenSubtask ? (
            <button
              type="button"
              className="tool-activity-jump"
              onClick={openSubtask}
              disabled={subtaskJumpState === 'loading'}
            >
              {subtaskJumpState === 'loading' ? '打开中…' : subtaskJumpState === 'missing' ? '暂未创建' : '查看子任务 ↗'}
            </button>
          ) : null}
          {stats ? (
            <span className="tool-activity-diff">
              {stats.added > 0 ? <span className="tool-activity-added">+{stats.added}</span> : null}
              {stats.removed > 0 ? <span className="tool-activity-removed">-{stats.removed}</span> : null}
            </span>
          ) : null}
          {running || failed ? (
            <span className={'tool-activity-phase ' + (running ? 'running' : 'error')}>{phaseLabel}</span>
          ) : null}
        </span>
      </div>
      {open ? (
        <div className="tool-activity-details">
          <div className="tool-activity-ledger">
            <span>工具 · <span className="tool-activity-engine">{name}</span></span>
            <span>{running ? '已提交 · 等待返回' : failed ? '执行失败' : '结果已记录'}</span>
          </div>
          <div className="tool-nested-stack">
            <ToolDetailSection id="result" label="结果" meta={running ? '等待中' : failed ? '失败' : '已完成'} stateKey={stateKey}>
              <ResultView name={name} args={args} result={result} artifact={browserResultArtifact} onOpenFile={onOpenFile} />
            </ToolDetailSection>
            <ToolDetailSection id="input" label="参数" meta={args ? 'JSON' : '空'} stateKey={stateKey}>
              <pre className="tool-json">{formatJSON(args) || rawArgs || '(无输入)'}</pre>
            </ToolDetailSection>
            <ToolDetailSection id="raw" label="原始数据" meta="JSON" stateKey={stateKey}>
              <>
                <div className="tool-activity-raw-label">工具调用</div>
                <pre className="tool-json">{rawArgs || '(无)'}</pre>
                <div className="tool-activity-raw-label">工具结果</div>
                <pre className="tool-json">{result ? formatJSON({ content: result.content, error: result.error }) : '(无)'}</pre>
              </>
            </ToolDetailSection>
          </div>
        </div>
      ) : null}
    </div>
  );
}

export function ToolProcessFold({ toolCalls, toolResults = [], llmStats = null, thinking = false, hasOutput = false, thinkingCount = 0, summaryCount = 0, outputCount = 0, children = null, orderedItems = null, auxiliaryCount = 0, onOpenSubtask = null, onOpenFile = null, stateKey = '' }) {
  const calls = Array.isArray(toolCalls) ? toolCalls : [];
  const results = Array.isArray(toolResults) ? toolResults : [];
  const thinkCount = thinkingCount || (thinking ? 1 : 0);
  const outCount = outputCount || (hasOutput ? 1 : 0);
  const hasOrderedItems = Array.isArray(orderedItems) && orderedItems.length > 0;
  const hasChildren = auxiliaryCount > 0 || React.Children.count(children) > 0;
  const hasProcessContent = calls.length > 0 || thinkCount > 0 || summaryCount > 0 || outCount > 0 || hasChildren;
  const running = calls.some((_, index) => !results[index]);
  const foldKey = stateKey ? `${stateKey}:process` : '';
  const [open, setOpen] = useState(() => readDisclosureState(foldKey, false));

  const toggleOpen = () => setOpen(value => {
    const next = !value;
    writeDisclosureState(foldKey, next);
    return next;
  });

  if (!hasProcessContent) return null;
  if (calls.length === 1 && thinkCount === 0 && summaryCount === 0 && outCount === 0 && !hasChildren) {
    return hasOrderedItems
      ? <>{orderedItems}{children}</>
      : <>{<ToolActivity tc={calls[0]} result={results[0]} onOpenSubtask={onOpenSubtask} onOpenFile={onOpenFile} stateKey={toolDisclosureKey(stateKey, calls[0], 0)} />}{children}</>;
  }

  const failed = results.some((result) => {
    if (!result) return false;
    if (result.error) return true;
    const text = typeof result.content === 'string' ? result.content : '';
    return /"error"\s*:/.test(text) || /^error\b/i.test(text.trim());
  });
  const toolStats = summarizeToolCalls(calls);
  const toolSummary = toolStats
    .map(item => (item.count > 1 ? `${item.name}×${item.count}` : item.name))
    .join('、');
  const summary = [
    summaryCount ? `摘要×${summaryCount}` : '',
    thinkCount ? `思考×${thinkCount}` : '',
    outCount ? `输出×${outCount}` : '',
    toolSummary ? `工具：${toolSummary}` : '',
  ].filter(Boolean).join(' · ');
  const summaryTitle = calls.length ? calls.map(toolCallTitle).join('\n') : '思考、摘要与执行过程';
  const totals = calls.reduce((acc, tc) => {
    const rawArgs = tc && tc.function && tc.function.arguments || '';
    const args = parseJSON(rawArgs);
    const stats = diffStats(tc && tc.function && tc.function.name, args);
    if (stats) {
      acc.added += stats.added;
      acc.removed += stats.removed;
    }
    return acc;
  }, { added: 0, removed: 0 });
  const promptTokens = Number(llmStats && llmStats.prompt_tokens) || 0;
  const completionTokens = Number(llmStats && llmStats.completion_tokens) || 0;
  const timestamps = [
    ...calls.map(call => Number(call && (call._timestamp || call.timestamp)) || 0),
    ...results.map(result => Number(result && (result.timestamp || result.ts)) || 0),
  ].filter(Boolean);
  const timestampSpanMs = timestamps.length > 1 ? Math.max(...timestamps) - Math.min(...timestamps) : 0;
  const llmDurationMs = Number(llmStats && llmStats.duration_ms) || 0;
  // ⏱ 展示这一段真实经历的时间跨度（首条调用 → 最后一条结果）。
  // LLM 调用耗时合计是各次调用相加，并行或含工具等待时与跨度并不相等，
  // 因此只在 tooltip 里展开，避免和「本轮耗时」混为一谈。
  const durationMs = timestampSpanMs || llmDurationMs;
  const metricParts = [];
  if (promptTokens || completionTokens) metricParts.push(`↑ ${formatCount(promptTokens)} ↓ ${formatCount(completionTokens)}`);
  if (durationMs > 0) metricParts.push(`⏱ ${formatDurationMs(durationMs)}`);
  const metricsTitle = `本摘要 LLM Token（这段工具调用各轮的 prompt / completion 累计）：↑ ${formatCount(promptTokens)} ↓ ${formatCount(completionTokens)}；本轮真实跨度（首条工具调用 → 最后一条工具结果，含工具执行与子任务等待）${formatDurationMs(timestampSpanMs)}；LLM 调用耗时合计（各次相加，不含工具等待）${formatDurationMs(llmDurationMs)}`;

  return (
    <div className={'tool-process-fold' + (open ? ' open' : '')}>
      <button type="button" className="tool-process-summary" onClick={toggleOpen} title={summaryTitle}>
        <span className="tool-process-toggle">{open ? '▾' : '▸'}</span>
        <span className="tool-process-tools">{summary}</span>
        {totals.added > 0 || totals.removed > 0 ? (
          <span className="tool-process-diff">
            {totals.added > 0 ? <span className="tool-activity-added">+{totals.added}</span> : null}
            {totals.removed > 0 ? <span className="tool-activity-removed">-{totals.removed}</span> : null}
          </span>
        ) : null}
        {metricParts.length ? <span className="tool-process-metrics" title={metricsTitle}>{metricParts.join(' · ')}</span> : null}
        {running ? <span className="tool-process-state running">执行中</span> : failed ? <span className="tool-process-state error">有失败</span> : null}
      </button>
      {open ? (
        <div className="tool-process-body">
          {hasOrderedItems ? orderedItems : (
            <>
              {calls.map((tc, index) => (
                <ToolActivity
                  key={(tc && tc.id) || index}
                  tc={tc}
                  result={results[index]}
                  onOpenSubtask={onOpenSubtask}
                  onOpenFile={onOpenFile}
                  stateKey={toolDisclosureKey(stateKey, tc, index)}
                />
              ))}
              {children}
            </>
          )}
        </div>
      ) : null}
    </div>
  );
}

// ToolProcessOverview is the interaction-level disclosure. It keeps progress
// text visible while hiding every nested command group when collapsed.
export function ToolProcessOverview({ toolCalls, toolResults = [], llmStats = null, thinkingCount = 0, outputCount = 0, children = null, stateKey = '', defaultOpen = false }) {
  const calls = Array.isArray(toolCalls) ? toolCalls : [];
  const results = Array.isArray(toolResults) ? toolResults : [];
  const foldKey = stateKey ? `${stateKey}:overview` : '';
  const [open, setOpen] = useState(() => readDisclosureState(foldKey, defaultOpen));
  const toggleOpen = () => setOpen(value => {
    const next = !value;
    writeDisclosureState(foldKey, next);
    return next;
  });

  if (calls.length === 0) {
    return typeof children === 'function' ? children(true) : children;
  }

  const running = calls.some((_, index) => !results[index]);
  const failed = results.some((result) => {
    if (!result) return false;
    if (result.error) return true;
    const text = typeof result.content === 'string' ? result.content : '';
    return /"error"\s*:/.test(text) || /^error\b/i.test(text.trim());
  });
  const toolStats = summarizeToolCalls(calls);
  const toolSummary = toolStats
    .map(item => (item.count > 1 ? `${item.name}×${item.count}` : item.name))
    .join('、');
  const summary = [
    thinkingCount ? `思考×${thinkingCount}` : '',
    outputCount ? `输出×${outputCount}` : '',
    toolSummary ? `工具：${toolSummary}` : '',
  ].filter(Boolean).join(' · ');
  const promptTokens = Number(llmStats && llmStats.prompt_tokens) || 0;
  const completionTokens = Number(llmStats && llmStats.completion_tokens) || 0;
  const llmDurationMs = Number(llmStats && llmStats.duration_ms) || 0;
  const timestamps = [
    ...calls.map(call => Number(call && (call._timestamp || call.timestamp)) || 0),
    ...results.map(result => Number(result && (result.timestamp || result.ts)) || 0),
  ].filter(Boolean);
  const timestampSpanMs = timestamps.length > 1 ? Math.max(...timestamps) - Math.min(...timestamps) : 0;
  const durationMs = timestampSpanMs || llmDurationMs;
  const metricParts = [];
  if (promptTokens || completionTokens) metricParts.push(`↑ ${formatCount(promptTokens)} ↓ ${formatCount(completionTokens)}`);
  if (durationMs > 0) metricParts.push(`⏱ ${formatDurationMs(durationMs)}`);

  return (
    <div className={'tool-process-fold tool-process-overview' + (open ? ' open' : '')}>
      <button type="button" className="tool-process-summary" onClick={toggleOpen}>
        <span className="tool-process-toggle">{open ? '▾' : '▸'}</span>
        <span className="tool-process-tools">{summary}</span>
        {metricParts.length ? <span className="tool-process-metrics">{metricParts.join(' · ')}</span> : null}
        {running ? <span className="tool-process-state running">执行中</span> : failed ? <span className="tool-process-state error">有失败</span> : null}
      </button>
      <div className="tool-process-body tool-process-overview-body">
        {typeof children === 'function' ? children(open) : children}
      </div>
    </div>
  );
}
