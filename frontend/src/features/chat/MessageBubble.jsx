import React, { useState } from 'react';
import { extractAssistantContent, extractThinkingContent, stripThinkingTags, stripReflectionTags, hasReflectionTag, hasReportTag, hasSummaryTag, extractSummaryContent, parsePptTaskFinished } from '../../api/chat';
import ReportCard from '../../components/ReportCard';
import { renderReportHtml } from '../../utils/reportHtml';
import ToolBlock from '../../components/ToolBlock';
import { ToolProcessFold } from '../../components/ToolActivity';
import ReflectionBlock from '../../components/ReflectionBlock';

export function filterForResultsOnly(msg) {
  if (msg.role === 'user') return msg;
  if (msg.role === 'tool') return null;
  if (msg.role === 'assistant') {
    if (hasSummaryTag(msg.content || '')) return null;
    if (msg.tool_calls && msg.tool_calls.length > 0) return null;
    return msg;
  }
  return msg;
}

function ToolCallView({ tc, result, onOpenFile }) {
  return <ToolBlock tc={tc} result={result} onOpenFile={onOpenFile} />;
}

// Render a standalone tool result message (role === 'tool') as a collapsible block.
function ToolResultMessage({ msg, onOpenFile }) {
  const fakeTc = { function: { name: msg.name || 'tool' } };
  return <ToolBlock tc={fakeTc} result={{ content: msg.content || '' }} onOpenFile={onOpenFile} />;
}

function isArchivedReportArtifact(file) {
  if (!file || typeof file !== 'object') return false;
  if (file.kind === 'report' || file.archivedReport === true) return true;
  const name = file.name || String(file.path || '').split(/[\\/]/).pop() || '';
  return /(?:^|[-_])report(?:-\d+)?\.m(?:d|markdown)$/i.test(name);
}

// Collapsible thinking-chain node for a <summary> (compressed history) message.
export function SummaryCard({ content }) {
  const [open, setOpen] = useState(false);
  const body = extractSummaryContent(content);
  const preview = body.replace(/\s+/g, ' ').trim().slice(0, 180);
  return (
    <div className="thinking-block summary-thinking-block">
      <div className="thinking-block-header" onClick={() => setOpen(!open)}>
        <span className="thinking-block-toggle">{open ? '▾' : '▸'}</span>
        <span className="thinking-block-label">SUMMARY // 上下文摘要</span>
        {!open && <span className="thinking-block-preview" title={body}>{preview}{body.length > 180 ? '…' : ''}</span>}
        <span className="thinking-block-btn">{open ? '收起' : '展开'}</span>
      </div>
      {open && <pre className="thinking-block-body">{body}</pre>}
    </div>
  );
}

export function ThinkingBlock({ text }) {
  const [open, setOpen] = useState(false);
  const body = String(text || '');
  const preview = body.slice(0, 140);
  return (
    <div className="thinking-block">
      <div className="thinking-block-header" onClick={() => setOpen(!open)}>
        <span className="thinking-block-toggle">{open ? '▾' : '▸'}</span>
        <span className="thinking-block-label">💭 思考过程</span>
        {!open && <span className="thinking-block-preview" title={body}>{preview}{body.length > 140 ? '…' : ''}</span>}
        <span className="thinking-block-btn">{open ? '收起' : '展开'}</span>
      </div>
      {open && <pre className="thinking-block-body">{body}</pre>}
    </div>
  );
}

// Convert <cite> tags into clickable links: url cites become <a target=_blank>,
// path cites keep their markdown link label as plain text.
function renderCites(text) {
  const parts = String(text || '').split(/(<cite\b[^>]*>[\s\S]*?<\/cite>)/gi);
  if (parts.length === 1) return text;
  return parts.map((part, i) => {
    if (!/<cite\b[^>]*>/i.test(part)) return part;
    const url = (part.match(/\burl="([^"]*)"/i) || [])[1];
    const title = (part.match(/\btitle="([^"]*)"/i) || [])[1] || '';
    const inner = part.replace(/^<cite\b[^>]*>/i, '').replace(/<\/cite>$/i, '');
    if (url) {
      return <a key={i} href={url} target="_blank" rel="noopener noreferrer" className="cite-link" title={title}>{inner}</a>;
    }
    return inner;
  });
}

function openLocalReportLink(event, onOpenFile) {
  if (!onOpenFile) return;
  const anchor = event.target && event.target.closest
    ? event.target.closest('a.report-file-link')
    : null;
  if (!anchor) return;
  const href = anchor.getAttribute('href') || '';
  if (!href.startsWith('/api/file-content?')) return;
  let pathValue = '';
  try {
    pathValue = new URL(href, window.location.origin).searchParams.get('path') || '';
  } catch {
    return;
  }
  if (!pathValue) return;
  event.preventDefault();
  onOpenFile({ path: pathValue, name: pathValue.split('/').pop() || '文件' });
}

function AssistantContent({ content, toolCalls, toolResults = [], llmStats = null, suppressReport = false, draft = false, replacedDraft = false, thinking, onOpenFile, suppressToolFold = false, suppressThinking = false, onOpenSubtask = null, suppressPptArtifact = false, reportArtifact = null, stateKey = '' }) {
  const rawContent = content || '';
  const bodyContent = stripThinkingTags(stripReflectionTags(rawContent));
  const thinkText = suppressThinking
    ? ''
    : (thinking && String(thinking).trim()) ? String(thinking).trim() : extractThinkingContent(rawContent);

  // Compressed-history markers belong to the thinking chain. Keep their full
  // body available without mixing it into the user-visible final answer.
  if (hasSummaryTag(bodyContent)) {
    return (
      <div className="assistant-thinking-chain">
        {thinkText && <ThinkingBlock text={thinkText} />}
        <SummaryCard content={rawContent} />
      </div>
    );
  }
  if (suppressReport && hasReportTag(bodyContent)) {
    return null;
  }
  if (hasReportTag(bodyContent)) {
    // A <reflection> inside the report is an internal self-check: render it as
    // the collapsible reflection card and strip it from the report body so the
    // reflection never surfaces as a separate user-facing "报告".
    const reflectionText = hasReflectionTag(rawContent) ? rawContent : bodyContent;
    const body = extractAssistantContent(bodyContent);
    const card = body.trim() && (body.length > 200 || /![\[]|<cite/i.test(body)
      ? <ReportCard bodyText={body} reportArtifact={reportArtifact} onOpenFile={onOpenFile} />
      : <pre>{renderCites(body)}</pre>);
    return (
      <div>
        {thinkText && <ThinkingBlock text={thinkText} />}
        {hasReflectionTag(reflectionText) && <ReflectionBlock text={reflectionText} />}
        {draft && <div className="report-draft-line">✍️ 草稿中…（反思后将更新为最终版）</div>}
        {card}
        {replacedDraft && <div className="report-updated-line">✅ 反思完成，已更新为最终报告</div>}
      </div>
    );
  }

  // Intermediate progress text is ordinary model output. Literal tags such as
  // <msg> are preserved rather than interpreted by the frontend.
  const displayText = extractAssistantContent(bodyContent);
  const pptFinished = parsePptTaskFinished(rawContent);
  const pptArtifact = pptFinished
    ? { kind: 'ppt', path: pptFinished.path, name: 'PPT 演示', ext: 'PPT' }
    : null;

  return (
    <div>
      {thinkText && <ThinkingBlock text={thinkText} />}
      {hasReflectionTag(rawContent) && <ReflectionBlock text={rawContent} />}
      {displayText && (
        <div
          className="reply-rendered"
          onClick={event => openLocalReportLink(event, onOpenFile)}
          dangerouslySetInnerHTML={{ __html: renderReportHtml(displayText) }}
        />
      )}
      {toolCalls && toolCalls.length > 0 && !suppressToolFold && (
        <ToolProcessFold
          toolCalls={toolCalls}
          toolResults={toolResults}
          llmStats={llmStats}
          thinking={Boolean(thinkText)}
          hasOutput={Boolean(displayText)}
          onOpenSubtask={onOpenSubtask}
          stateKey={stateKey}
        />
      )}
      {pptArtifact && !suppressPptArtifact && <ArtifactResultCards files={[pptArtifact]} onOpenFile={onOpenFile} />}
    </div>
  );
}

function ArtifactResultCards({ files, onOpenFile }) {
  if (!Array.isArray(files) || files.length === 0) return null;
  return (
    <div className="artifact-result-list" aria-label="生成结果">
      {files.map((file, index) => {
        const name = file.name || String(file.path || '').split(/[\\/]/).pop() || '生成结果';
        const ext = (file.ext || (name.match(/\.([a-z0-9]+)$/i) || [,'FILE'])[1]).toUpperCase();
        const isPPT = file.kind === 'ppt';
        return (
          <button
            key={(file.path || name) + index}
            type="button"
            className="artifact-result-card"
            onClick={() => onOpenFile && onOpenFile(file)}
            title={file.path || name}
          >
            <span className="artifact-result-icon" aria-hidden="true">
              {isPPT ? (
                <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
                  <rect x="3" y="4" width="18" height="13" rx="2" />
                  <path d="M8 21h8M12 17v4" />
                  <path d="M8 12l2-2 2 2 3-3" />
                </svg>
              ) : (
                <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
                  <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" />
                  <path d="M14 2v6h6" />
                  <path d="M8 14h8M8 18h6" />
                </svg>
              )}
              <span className="artifact-result-ext">{ext}</span>
            </span>
            <span className="artifact-result-info">
              <span className="artifact-result-label">生成结果</span>
              <span className="artifact-result-name">{name}</span>
            </span>
            <span className="artifact-result-action">查看</span>
          </button>
        );
      })}
    </div>
  );
}

export default function MessageBubble({ msg, mode, onOpenFile, artifacts, suppressToolFold = false, suppressThinking = false, onOpenSubtask = null, stateKey = '' }) {
  if (mode === 'results-only') {
    const filtered = filterForResultsOnly(msg);
    if (!filtered) return null;
  }
  const role = msg.role;
  const hasInlineReport = role === 'assistant' && hasReportTag(msg.content || '');
  const incomingArtifacts = Array.isArray(artifacts) ? artifacts : [];
  const reportArtifact = incomingArtifacts.find(isArchivedReportArtifact) || null;
  const useReportArtifact = Boolean(reportArtifact && hasInlineReport);
  const pptPath = parsePptTaskFinished(msg.content || '');
  const pptPathStr = pptPath && pptPath.path ? String(pptPath.path) : '';
  const visibleArtifacts = (useReportArtifact
    ? incomingArtifacts.filter(file => !isArchivedReportArtifact(file))
    : incomingArtifacts
  ).filter(file => !(pptPathStr && file && String(file.path || '') === pptPathStr));
  return (
    <div className={'msg msg-' + role}>
      {role === 'user' && <div className="role-label">{role}</div>}
      <div className="bubble">
        {role === 'user' && <pre>{msg.content}</pre>}
        {role === 'user' && Array.isArray(msg.files) && msg.files.length > 0 && (
          <div className="msg-files">
            {msg.files.map((f, i) => (
              <button
                type="button"
                className="msg-file-chip"
                key={(f.path || '') + i}
                title={f.path || ''}
                onClick={() => onOpenFile && onOpenFile(f)}
                aria-label={'预览 ' + (f.name || String(f.path || '').split('/').pop() || '附件')}
              >
                <svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor"
                  strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                  <path d="M21.44 11.05l-9.19 9.19a6 6 0 0 1-8.49-8.49l9.19-9.19a4 4 0 0 1 5.66 5.66l-9.2 9.19a2 2 0 0 1-2.83-2.83l8.49-8.48" />
                </svg>
                <span className="msg-file-name">{f.name || String(f.path || '').split('/').pop()}</span>
              </button>
            ))}
          </div>
        )}
        {role === 'user' && msg.real_ms ? <div className='stats-per-message'>⏱ real {(msg.real_ms/1000).toFixed(1) + 's'}</div> : null}
        {role === 'assistant' && <>
        <AssistantContent content={msg.content || ''} toolCalls={msg.tool_calls} toolResults={msg._pairedResults || msg._streamResults} llmStats={msg.usage || msg.duration_ms ? { prompt_tokens: msg.usage?.prompt_tokens || 0, completion_tokens: msg.usage?.completion_tokens || 0, duration_ms: msg.duration_ms || 0 } : null} suppressToolFold={suppressToolFold} suppressThinking={suppressThinking} suppressReport={msg._suppressReport} draft={msg.draft} replacedDraft={msg.replacedDraft} thinking={msg.thinking} onOpenFile={onOpenFile} onOpenSubtask={onOpenSubtask} suppressPptArtifact={visibleArtifacts.some(file => file && file.kind === 'ppt')} reportArtifact={useReportArtifact ? reportArtifact : null} stateKey={stateKey} />
        </>}
        {role === 'tool' && !msg._suppress && <ToolResultMessage msg={msg} onOpenFile={onOpenFile} />}
        {role === 'system' && <pre className="system-text">{msg.content}</pre>}
      </div>
    </div>
  );
}
