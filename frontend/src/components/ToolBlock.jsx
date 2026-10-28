import React, { useState, useContext } from 'react';
import ToolActivity from './ToolActivity';
import ReportCard from './ReportCard';

export const SubtaskStreamContext = React.createContext({});

function fmt(v) {
  if (!v) return '';
  if (typeof v === 'string') {
    try { return JSON.stringify(JSON.parse(v), null, 2); }
    catch { return v; }
  }
  return JSON.stringify(v, null, 2);
}

function tryParseResult(content) {
  if (typeof content !== 'string') return content || null;
  try { return JSON.parse(content); } catch { return null; }
}

function stripTags(text) {
  return String(text || '')
    .replace(/<think[\s\S]*?<\/think>\s*/gi, '')
    .replace(/<report[\s\S]*?<\/report>\s*/gi, '')
    .replace(/<reflection[\s\S]*?<\/reflection>\s*/gi, '')
    .replace(/<[^>]+>/g, ' ')
    .replace(/\s+/g, ' ')
    .trim();
}

function extractReportBody(text) {
  const m = String(text || '').match(/<report(?:\s[^>]*)?>([\s\S]*?)<\/report>/i);
  return m ? m[1].trim() : null;
}

function roleLabel(role) {
  if (role === 'user') return '用户';
  if (role === 'assistant') return 'AI';
  if (role === 'tool') return '工具';
  return role || '?';
}

function UsageStats({ m }) {
  const u = m && m.usage;
  if (!u) return null;
  const pt = u.prompt_tokens ?? '?';
  const ct = u.completion_tokens ?? '?';
  const dur = m.duration_ms ? (m.duration_ms / 1000).toFixed(1) + 's' : '?';
  return (
    <div className="subtask-msg-stats">
      ↑ {pt} in &nbsp;↓ {ct} out &nbsp;⏱ {dur}
    </div>
  );
}


// Aggregate stats across ALL assistant messages of a finished subtask, so the
// card header shows the subtask total tokens / agent time (not just the last
// report message).
function SubtaskTotalStats({ msgs, agentStats }) {
  // Prefer the complete stats the subtask tool returns (computed from the full
  // session file); fall back to summing the compact message snapshot.
  let pt = 0, ct = 0, durMs = 0, has = false;
  if (agentStats) {
    pt = agentStats.prompt_tokens || 0;
    ct = agentStats.completion_tokens || 0;
    durMs = agentStats.duration_ms || 0;
    has = true;
  } else if (Array.isArray(msgs) && msgs.length > 0) {
    for (const m of msgs) {
      if (m && m.role === 'assistant' && m.usage) {
        has = true;
        pt += m.usage.prompt_tokens || 0;
        ct += m.usage.completion_tokens || 0;
      }
      if (m && typeof m.duration_ms === 'number') durMs += m.duration_ms;
    }
  }
  if (!has) return null;
  const dur = durMs ? (durMs / 1000).toFixed(1) + 's' : '?';
  return (
    <div className="subtask-msg-stats subtask-total-stats">
      in {pt.toLocaleString()} &nbsp;out {ct.toLocaleString()} &nbsp;agent {dur}
    </div>
  );
}
function subtaskTitleFromArgs(argsStr) {
  const parsed = tryParseResult(argsStr);
  return (parsed && parsed.title) || '';
}

// 简单 LCS 行级 diff，用于展示文件修改的 before/after。
function lcsDiff(oldLines, newLines) {
  const n = oldLines.length, m = newLines.length;
  const dp = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      dp[i][j] = oldLines[i] === newLines[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    }
  }
  const out = [];
  let i = 0, j = 0;
  while (i < n && j < m) {
    if (oldLines[i] === newLines[j]) { out.push({ type: 'same', text: oldLines[i] }); i++; j++; }
    else if (dp[i + 1][j] >= dp[i][j + 1]) { out.push({ type: 'del', text: oldLines[i] }); i++; }
    else { out.push({ type: 'add', text: newLines[j] }); j++; }
  }
  while (i < n) { out.push({ type: 'del', text: oldLines[i] }); i++; }
  while (j < m) { out.push({ type: 'add', text: newLines[j] }); j++; }
  return out;
}

// 文件修改 diff 视图：edit_file 显示行级 before/after，write_file 显示写入内容。
function FileEditDiff({ name, args }) {
  if (!args) return null;
  const pathLabel = args.file_path || args.path || '';
  if (name === 'edit_file' && typeof args.old_text === 'string' && typeof args.new_text === 'string') {
    const diff = lcsDiff(String(args.old_text).split('\n'), String(args.new_text).split('\n'));
    return (
      <div className="file-diff">
        <div className="file-diff-title">文件变更 {pathLabel}</div>
        <pre className="file-diff-body">
          {diff.map((d, idx) => (
            <div key={idx} className={'diff-line diff-' + d.type}>{d.type === 'add' ? '+' : d.type === 'del' ? '-' : ' '} {d.text}</div>
          ))}
        </pre>
      </div>
    );
  }
  if (name === 'write_file' && typeof args.content === 'string') {
    return (
      <div className="file-diff">
        <div className="file-diff-title">文件已写入 {pathLabel}</div>
        <pre className="file-diff-body">
          {String(args.content).split('\n').map((l, idx) => <div key={idx} className="diff-line diff-add">+ {l}</div>)}
        </pre>
      </div>
    );
  }
  return null;
}

// 通用工具折叠块（含嵌套的子代理工具）
function GenericToolBlock({ tc, result }) {
  return <ToolActivity tc={tc} result={result} />;
}

// 把子代理的一条 assistant(tool_calls) + 后续 tool 响应渲染成嵌套工具块组
function NestedToolGroup({ assistantMsg, results }) {
  const calls = Array.isArray(assistantMsg.tool_calls) ? assistantMsg.tool_calls : [];
  return (
    <div className="subtask-tools">
      <UsageStats m={assistantMsg} />
      {calls.map((tc, i) => (
        <GenericToolBlock key={i} tc={tc} result={results[i] || null} />
      ))}
    </div>
  );
}

// 子任务专用折叠块：头部 "子任务: 标题 详情▸/▾"，展开显示子代理执行记录（含嵌套工具块）
// Render live subtask execution events (status/assistant/tool_call) streamed
// from the child agent while it is still running.
function LiveStream({ events }) {
  if (!events || events.length === 0) {
    return <div className="subtask-running">⏳ 子任务执行中，请稍候...</div>;
  }
  return (
    <div className="subtask-live">
      {events.map((ev, i) => {
        if (ev.type === 'subtask_start') return null;
        if (ev.type === 'status') {
          return (
            <div key={i} className="subtask-live-status">
              <span className="subtask-live-dot">●</span>
              <span className="subtask-live-text">{ev.message || ''}</span>
            </div>
          );
        }
        if (ev.type === 'assistant') {
          const text = stripTags(ev.content || '');
          const calls = Array.isArray(ev.tool_calls)
            ? ev.tool_calls.map(tc => tc.function?.name || '?').join(', ')
            : '';
          return (
            <div key={i} className="subtask-live-assistant">
              {calls ? <span className="subtask-live-toolcall">⚙ 调用: {calls}</span> : (text || null)}
            </div>
          );
        }
        if (ev.type === 'tool_call') {
          let args = '';
          try { args = fmt(ev.arguments); } catch {}
          const preview = String(args || '').slice(0, 200);
          return (
            <div key={i} className="subtask-live-tool">
              <span className="subtask-live-toolname">{ev.status === 'start' ? '▶' : (ev.ok ? '✓' : '✗')} {ev.tool}</span>
              {ev.status === 'start' && preview && <pre className="subtask-live-args">{preview}</pre>}
              {ev.status === 'end' && ev.result_preview && (
                <pre className="subtask-live-result">{String(ev.result_preview).slice(0, 300)}</pre>
              )}
              {ev.status === 'end' && ev.error && <div className="subtask-live-error">{String(ev.error).slice(0, 300)}</div>}
            </div>
          );
        }
        return null;
      })}
    </div>
  );
}

// Compact rendering for a finished subtask (history load). Mirrors the live
// stream look: tool rows + short assistant thoughts, with the final <report>
// kept intact for reading.
function HistoryStream({ msgs, onOpenFile }) {
  if (!msgs || msgs.length === 0) return null;
  const rows = [];
  let i = 0;
  while (i < msgs.length) {
    const m = msgs[i];
    const calls = Array.isArray(m.tool_calls) && m.tool_calls.length > 0 ? m.tool_calls : null;
    if (m.role === 'assistant' && calls) {
      const names = calls.map(tc => tc.function?.name || '?').join(', ');
      rows.push(
        <div key={'a' + i} className="subtask-live-assistant">
          <span className="subtask-live-toolcall">⚙ 调用: {names}</span>
        </div>
      );
      // collect following tool responses
      let j = i + 1;
      while (j < msgs.length && msgs[j].role === 'tool') {
        const tr = msgs[j];
        let preview = String(tr.content || '');
        if (preview.length > 300) preview = preview.slice(0, 300) + '...';
        const ok = !/\"error\"/.test(tr.content || '');
        rows.push(
          <div key={'t' + j} className="subtask-live-tool">
            <span className="subtask-live-toolname">{ok ? '✓' : '✗'} {tr.name || 'tool'}</span>
            {preview && <pre className="subtask-live-result">{preview}</pre>}
          </div>
        );
        j++;
      }
      i = j;
    } else if (m.role === 'assistant') {
      const raw = m.content || '';
      if (/<report\b/i.test(raw)) {
        const body = extractReportBody(raw);
        rows.push(
          <div key={'r' + i} className="subtask-report-artifact-wrap">
            <ReportCard bodyText={body || ''} onOpenFile={onOpenFile} />
            {m.usage && <UsageStats m={m} />}
          </div>
        );
      } else {
        const text = stripTags(raw);
        rows.push(
          <div key={'s' + i} className="subtask-live-assistant">
            {text || null}
          </div>
        );
      }
      i++;
    } else if (m.role === 'tool') {
      // orphan tool response (no matching assistant message)
      let preview = String(m.content || '');
      if (preview.length > 300) preview = preview.slice(0, 300) + '...';
      rows.push(
        <div key={'o' + i} className="subtask-live-tool">
          <span className="subtask-live-toolname">✓ {m.name || 'tool'}</span>
          {preview && <pre className="subtask-live-result">{preview}</pre>}
        </div>
      );
      i++;
    } else {
      i++; // skip user/system wrappers in compact view
    }
  }
  return <div className="subtask-live">{rows}</div>;
}

function SubtaskBlock({ tc, result, onOpenFile }) {
  const [open, setOpen] = useState(false);
  const allStreams = useContext(SubtaskStreamContext);
  const title = subtaskTitleFromArgs(tc && tc.function && tc.function.arguments) || '子任务';
  const parsedResult = result ? tryParseResult(result.content) : null;
  const msgs = parsedResult && Array.isArray(parsedResult.messages) ? parsedResult.messages : null;
  const ok = parsedResult ? parsedResult.ok : null;
  const error = (parsedResult && parsedResult.error) || (result && result.error) || '';

  // 构建嵌套视图：assistant(tool_calls) 与其后 tool 响应配对；普通消息单独渲染
  function buildRows(msgs) {
    const rows = [];
    let i = 0;
    while (i < msgs.length) {
      const m = msgs[i];
      const calls = Array.isArray(m.tool_calls) && m.tool_calls.length > 0 ? m.tool_calls : null;
      if (m.role === 'assistant' && calls) {
        // 收集紧随其后的 tool 响应（按 tool_call_id 或顺序）
        const results = [];
        let j = i + 1;
        while (j < msgs.length && msgs[j].role === 'tool') {
          const target = calls[results.length] ? calls[results.length].id : null;
          if (target && msgs[j].tool_call_id && msgs[j].tool_call_id !== target) break;
          results.push({ content: msgs[j].content || '', tool_call_id: msgs[j].tool_call_id || '', name: msgs[j].name || '' });
          j++;
        }
        rows.push({ type: 'tools', assistantMsg: m, results });
        i = j;
      } else if (m.role === 'tool') {
        // 孤立 tool 消息（异常情况）
        rows.push({ type: 'text', role: m.role, text: m.content || '' });
        i++;
      } else {
        const isReport = m.role === 'assistant' && /<report(?:\s[^>]*)?>/i.test(m.content || '');
        rows.push({ type: 'text', role: m.role, text: m.content || '', msg: m, isReport });
        i++;
      }
    }
    return rows;
  }

  const rows = msgs ? buildRows(msgs) : [];

  return (
    <div className="subtask-block">
      <div className="subtask-block-header" onClick={() => setOpen(!open)}>
        <span className="subtask-block-toggle">{open ? '▾' : '▸'}</span>
        <span className="subtask-block-icon">⤵</span>
        <span className="subtask-block-name">子任务: {title}</span>
        <span className="subtask-block-status">{ok === false ? '失败' : (result ? (msgs ? msgs.length + ' 条' : '完成') : '执行中...')}</span>
        <SubtaskTotalStats msgs={msgs} agentStats={parsedResult && parsedResult.agent_stats} />
        <span className="subtask-block-detail">{open ? '收起' : '详情'} {open ? '▲' : '▼'}</span>
      </div>
      {open && (
        <div className="subtask-block-body">
          {error && (
            <div className="subtask-block-error">{error}</div>
          )}
          {msgs && msgs.length > 0 ? (
            <HistoryStream msgs={msgs} onOpenFile={onOpenFile} />
          ) : result ? (
            <pre className="tool-block-code">{fmt(result.content)}</pre>
          ) : (
            <LiveStream events={allStreams[title]} />
          )}
          {parsedResult && parsedResult.session && (
            <div className="subtask-block-session">session: {parsedResult.session}</div>
          )}
        </div>
      )}
    </div>
  );
}

export default function ToolBlock({ tc, result, onOpenFile }) {
  const name = tc.function?.name || '';
  if (name === 'create_subtask') {
    return <SubtaskBlock tc={tc} result={result} onOpenFile={onOpenFile} />;
  }
  return <GenericToolBlock tc={tc} result={result} />;
}
