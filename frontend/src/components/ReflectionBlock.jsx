import React, { useState } from 'react';
import { extractReflectionBlocks } from '../api/chat';

function cleanText(text) {
  return String(text || '')
    .replace(/<cite\b[^>]*>[\s\S]*?<\/cite>/gi, (m) => {
      const idx = (m.match(/index="(\d+)"/) || [])[1];
      return idx ? `[${idx}]` : '';
    })
    .replace(/<[^>]+>/g, '')
    .replace(/[ \t]+/g, ' ')
    .trim();
}

function ReflectionItem({ item, idx }) {
  return (
    <div className="reflection-item" key={idx}>
      {item.category && <div className="reflection-item-category">{cleanText(item.category)}</div>}
      {item.finding && <div className="reflection-item-finding">{cleanText(item.finding)}</div>}
      {item.source && <div className="reflection-item-source">来源：{cleanText(item.source)}</div>}
    </div>
  );
}

// <reflection> 反思卡片：仿工具块折叠样式，结构化展示反思结果。
export default function ReflectionBlock({ text }) {
  const [open, setOpen] = useState(false);
  const blocks = extractReflectionBlocks(text);
  if (!blocks.length) return null;
  const first = blocks[0];
  const preview = cleanText(first.result || first.original_task || '反思完成');
  return (
    <div className="tool-block reflection-block">
      <div className="tool-block-header" onClick={() => setOpen(!open)}>
        <span className="tool-block-toggle">{open ? '▾' : '▸'}</span>
        <span className="tool-block-icon">🧠</span>
        <span className="tool-block-name">反思</span>
        <span className="tool-block-status ok">✓</span>
      </div>
      {!open && <div className="reflection-preview">{preview}{preview.length > 120 ? '…' : ''}</div>}
      {open && (
        <div className="tool-block-details">
          {blocks.map((b, i) => (
            <div className="reflection-card" key={i}>
              {b.original_task && (
                <div className="tool-block-section">
                  <div className="tool-block-section-title">反思目标</div>
                  <pre className="tool-block-code">{cleanText(b.original_task)}</pre>
                </div>
              )}
              {b.findings && b.findings.length > 0 && (
                <div className="tool-block-section">
                  <div className="tool-block-section-title">发现（{b.findings.length}）</div>
                  {b.findings.map((item, j) => <ReflectionItem key={j} item={item} idx={j} />)}
                </div>
              )}
              {b.result && (
                <div className="tool-block-section">
                  <div className="tool-block-section-title">结论</div>
                  <pre className="tool-block-code">{cleanText(b.result)}</pre>
                </div>
              )}
              {b.cite_files && (
                <div className="tool-block-section">
                  <div className="tool-block-section-title">引用</div>
                  <pre className="tool-block-code">{cleanText(b.cite_files)}</pre>
                </div>
              )}
              {b.plan && b.plan !== "[]" && (
                <div className="tool-block-section">
                  <div className="tool-block-section-title">后续计划</div>
                  <pre className="tool-block-code">{cleanText(b.plan)}</pre>
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
