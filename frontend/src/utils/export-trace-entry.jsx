// 导出 Trace 视图的入口：接收注入数据，原样渲染完整 TracePanel
import React from 'react';
import { createRoot } from 'react-dom/client';
import TracePanel from '../components/TracePanel';

export function renderTrace(rootEl, data, name) {
  createRoot(rootEl).render(
    React.createElement(TracePanel, { name: name || 'trace', onClose: () => {}, data }),
  );
}
