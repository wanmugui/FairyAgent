import React, { useCallback, useEffect, useState } from 'react';
import { useAskAutoConfirm } from './useAskAutoConfirm';

export default function PPTOutlineConfirmModal({ session, onComplete, onSkipAll }) {
  const [feedback, setFeedback] = useState('');
  const [outline, setOutline] = useState({ loading: true, content: '', error: '', truncated: false });
  const confirm = useCallback(
    () => onComplete({ confirmed: true, askType: 'ppt_mode.confirm_outline' }),
    [onComplete],
  );
  const { secondsLeft, resetCountdown, confirm: autoConfirm, cancel } = useAskAutoConfirm(confirm, session);
  const skip = useCallback(() => {
    cancel();
    onSkipAll();
  }, [cancel, onSkipAll]);

  useEffect(() => {
    const controller = new AbortController();
    setOutline({ loading: true, content: '', error: '', truncated: false });
    fetch('/api/sessions/' + encodeURIComponent(session) + '/ppt-outline', { signal: controller.signal })
      .then(async response => {
        const data = await response.json();
        if (!response.ok) throw new Error(data.error || '无法读取大纲');
        setOutline({ loading: false, content: data.content || '', error: '', truncated: !!data.truncated });
      })
      .catch(error => {
        if (error.name !== 'AbortError') {
          setOutline({ loading: false, content: '', error: error.message || '无法读取大纲', truncated: false });
        }
      });
    return () => controller.abort();
  }, [session]);

  const submitFeedback = () => {
    const value = feedback.trim();
    if (!value) return;
    onComplete({ outlineFeedback: value });
  };

  return (
    <div className="ask-modal-overlay">
      <div className="ask-modal-card ppt-outline-modal">
        <div className="ask-modal-header">
          <div className="ask-modal-title">确认 PPT 大纲</div>
          <div className="ppt-config-hint">大纲已生成并通过校验。确认后将开始制作页面。</div>
        </div>

        <div className="ppt-outline-confirm-body">
          <strong>是否按当前大纲继续？</strong>
          <p>如需调整，请直接写出要修改的页面、顺序或内容重点。</p>
          <div className="ppt-outline-preview" aria-live="polite">
            {outline.loading && '正在加载大纲…'}
            {!outline.loading && outline.error && '大纲预览不可用：' + outline.error}
            {!outline.loading && !outline.error && <pre>{outline.content}</pre>}
            {outline.truncated && <div className="ppt-outline-truncated">大纲内容较长，当前仅显示前半部分。</div>}
          </div>
          <textarea
            autoFocus
            value={feedback}
            placeholder="例如：第 3 页改为案例分析，并增加一页实施计划"
            onChange={event => { setFeedback(event.target.value); resetCountdown(); }}
            onKeyDown={event => {
              if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') submitFeedback();
            }}
          />
        </div>

        <div className="ask-modal-footer">
          <span className="ask-modal-skip" onClick={skip}>取消</span>
          <button className="ask-modal-secondary" disabled={!feedback.trim()} onClick={submitFeedback}>提交修改</button>
          <button className="ask-modal-next" onClick={autoConfirm}>确认并继续（{secondsLeft}s）</button>
        </div>
      </div>
    </div>
  );
}
