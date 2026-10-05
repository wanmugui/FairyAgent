import React, { useState } from 'react';
import { renderReportHtml } from '../utils/reportHtml';

function reportTitle(bodyText) {
  const raw = String(bodyText || '');
  const markdown = raw.match(/^\s*#{1,3}\s+(.+)$/m);
  const html = raw.match(/<h[1-3][^>]*>([\s\S]*?)<\/h[1-3]>/i);
  const title = markdown && markdown[1] || html && html[1] || '';
  const clean = title.replace(/<[^>]+>/g, '').replace(/\s+/g, ' ').trim();
  return clean ? clean.slice(0, 72) : '报告详情';
}

export default function ReportCard({ bodyText, onOpenFile, reportArtifact = null }) {
  const [open, setOpen] = useState(false);
  const plain = String(bodyText || '').replace(/<cite\b[^>]*>[\s\S]*?<\/cite>/gi, (m) => { const i = (m.match(/index="(\d+)"/) || [,''])[1]; return '[' + i + ']'; }).replace(/<[^>]+>/g, '');
  const preview = plain.length > 300 ? plain.slice(0, 300) + '...' : plain;
  const html = renderReportHtml(bodyText);
  const title = reportTitle(bodyText);
  const openReport = () => {
    if (onOpenFile) {
      const target = { kind: 'report', name: title, bodyText };
      if (reportArtifact && reportArtifact.path) {
        target.path = reportArtifact.path;
        target.archived = true;
      }
      onOpenFile(target);
      return;
    }
    setOpen(true);
  };
  const openLocalLink = event => {
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
  };
  return (
    <>
      <div className="artifact-result-list report-artifact-list">
        <button
          type="button"
          className="artifact-result-card report-artifact-card"
          onClick={openReport}
          title={preview || title}
        >
          <span className="artifact-result-icon report-artifact-icon" aria-hidden="true">
            <svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
              <path d="M5 3h11l3 3v15H5z" />
              <path d="M16 3v4h4M8 16v-3M12 16V9M16 16v-5" />
            </svg>
            <span className="artifact-result-ext">RPT</span>
          </span>
          <span className="artifact-result-info">
            <span className="artifact-result-label">REPORT // 分析报告</span>
            <span className="artifact-result-name">{title}</span>
          </span>
          <span className="artifact-result-action">右侧查看</span>
        </button>
      </div>
      {open && (
        <div className="report-modal-overlay" onClick={() => setOpen(false)}>
          <div className="report-modal-box" onClick={e => e.stopPropagation()}>
            <div className="report-modal-header">
              <span><svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6"/><line x1="8" y1="13" x2="16" y2="13"/><line x1="8" y1="17" x2="16" y2="17"/></svg>报告详情</span>
              <button className="report-modal-close" onClick={() => setOpen(false)}>&times;</button>
            </div>
            <div className="report-modal-body report-rendered" onClick={openLocalLink} dangerouslySetInnerHTML={{ __html: html }} />
          </div>
        </div>
      )}
    </>
  );
}
