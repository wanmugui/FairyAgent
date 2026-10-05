import React from 'react';

export default function VersionNav({ mode, onChange }) {
  return (
    <nav style={{
      position: 'fixed', left: '12px', bottom: '12px', zIndex: 100,
      display: 'flex', gap: '6px', background: '#fff',
      border: '1px solid #d1d5db', borderRadius: '8px', padding: '5px',
      boxShadow: '0 2px 8px rgba(0,0,0,.12)', font: '12px sans-serif'
    }}>
      {[
        { id: 'all-tags', label: '全量标签版', color: '#7c2d12', bg: '#fef3c7' },
        { id: 'results-only', label: '仅结果版', color: '#166534', bg: '#dcfce7' },
      ].map(v => (
        <a key={v.id} href="#!"
          onClick={e => { e.preventDefault(); onChange(v.id); }}
          style={{
            padding: '5px 8px', cursor: 'pointer',
            color: mode === v.id ? v.color : '#1f2937',
            textDecoration: 'none',
            fontWeight: mode === v.id ? 700 : 400,
            background: mode === v.id ? v.bg : 'transparent',
            borderRadius: '4px',
          }}
        >{v.label}</a>
      ))}
    </nav>
  );
}
