const PHASE_LABELS = {
  waiting_response: '等待响应',
  first_delta: '收到首包',
  streaming_response: '接收正文',
  tool_arguments: '生成工具参数',
  response_complete: '响应完成',
  response_error: '响应异常',
  status: '状态',
};

function formatDuration(ms) {
  const value = Number(ms) || 0;
  if (value >= 60000) return `${(value / 60000).toFixed(1)}m`;
  if (value >= 1000) return `${(value / 1000).toFixed(1)}s`;
  return `${value}ms`;
}

function formatBytes(bytes) {
  const value = Number(bytes) || 0;
  if (value >= 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(1)} MB`;
  if (value >= 1024) return `${(value / 1024).toFixed(1)} KB`;
  return `${value} B`;
}

export function formatLLMProgress(progress) {
  if (!progress) return null;
  const phase = progress.phase || 'waiting_response';
  if (phase === 'status' && progress.status_text) {
    return {
      phase: PHASE_LABELS.status,
      detail: String(progress.status_text),
      title: String(progress.status_text),
    };
  }
  return {
    phase: PHASE_LABELS[phase] || phase,
    detail: [
      formatDuration(progress.elapsed_ms),
      `Δ ${formatDuration(progress.since_last_delta_ms)}`,
      formatBytes(progress.received_bytes),
      `${Number(progress.chunk_count) || 0} chunks`,
    ].join(' · '),
    title: [
      `phase=${phase}`,
      `elapsed=${Number(progress.elapsed_ms) || 0}ms`,
      `since_last_delta=${Number(progress.since_last_delta_ms) || 0}ms`,
      `received=${Number(progress.received_bytes) || 0}B`,
      `content=${Number(progress.content_bytes) || 0}B`,
      `tool_args=${Number(progress.tool_arguments_bytes) || 0}B`,
      `chunks=${Number(progress.chunk_count) || 0}`,
    ].join(' '),
  };
}
