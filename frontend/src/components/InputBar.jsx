import React, { forwardRef, useCallback, useEffect, useImperativeHandle, useState, useRef } from 'react';
import { createPortal } from 'react-dom';
import { uploadFile, fetchModels, updateModel } from '../api/chat';

function fileExtension(name) {
  const value = String(name || '').trim();
  const dot = value.lastIndexOf('.');
  if (dot < 1 || dot === value.length - 1) return 'FILE';
  return value.slice(dot + 1).slice(0, 4).toUpperCase();
}

function stripInternalSummaryBlocks(value) {
  return String(value || '')
    .replace(/<summary\b[^>]*>[\s\S]*?<\/summary\s*>/gi, '')
    .replace(/<\/?summary\b[^>]*>/gi, '')
    .trim();
}

// Chat input bar.
//   - typing into the box is always allowed, even while the agent is streaming.
//   - Enter inserts the message: when the agent is idle this starts a fresh
//     turn; while it is streaming the message goes through the inject channel
//     so the agent picks it up between steps without losing its current work.
//   - Stop button: "soft stop" — finishes the in-flight step, drains any
//     queued user messages into a fresh turn, then exits. Use this when the
//     user wants to interrupt and switch topic. Esc triggers the same path.
//   - Long-press Stop (or Shift+Esc) triggers a hard cancel that exits the
//     agent immediately, abandoning the in-flight work.
//   - Files attach via the paperclip or drag-and-drop; they upload to the
//     server first and travel with the next message as <file_context>.
const InputBar = forwardRef(function InputBar(
  { onSend, loading, onAbort, sessionName, onEnsureSession,
    models: modelsProp, selectedModel: selectedModelProp, onModelChange },
  ref,
) {
  const [text, setText] = useState('');
  const [hardCancelArmed, setHardCancelArmed] = useState(false);
  const [attachments, setAttachments] = useState([]);
  const [uploading, setUploading] = useState(false);
  // 推理强度：放在对话框里而不是设置菜单，因为它是每轮对话都会临时调的参数。
  // 按模型分别保存；关闭 = 不发送 reasoning_effort 字段。
  const EFFORT_TIERS = [
    { value: '', label: '关闭' },
    { value: 'low', label: '低' },
    { value: 'medium', label: '中' },
    { value: 'high', label: '高' },
  ];
  const [fetchedModels, setFetchedModels] = useState([]);
  const [effortNotice, setEffortNotice] = useState('');
  const reportEffortError = useCallback((msg) => {
    setEffortNotice(msg);
    window.clearTimeout(reportEffortError._t);
    reportEffortError._t = window.setTimeout(() => setEffortNotice(''), 4000);
  }, []);
  const [effortOpen, setEffortOpen] = useState(false);
  // 推理强度浮层原来挂在输入框内部（.chat-input-box 是 overflow:hidden），
  // 浮层向上弹出时被裁掉一截，只能看到下半部分。改成 portal 到 body，
  // 用 fixed 坐标自己定位，脱离任何 overflow 裁切。
  const effortTriggerRef = useRef(null);
  const effortPanelRef = useRef(null);
  const [effortPanelPos, setEffortPanelPos] = useState(null);
  // Codex 式滑块：轨道上一个圆点可左右拖动，松手吸附到最近一档。点文字也算
  // 一次跳转，但拖动是主交互——只有四个可点按钮的话不叫滑块。
  const [effortDragging, setEffortDragging] = useState(false);
  // 拖动期间只动本地预览，松手才发一次请求。每次 pointermove 都打接口会
  // 并发写同一个模型，响应乱序后档位会跳。
  const [effortPreview, setEffortPreview] = useState(null);
  const effortTrackRef = useRef(null);

  // 浮层贴到触发器正上方，靠左对齐；靠右时用 maxWidth 夹在视口内。
  useEffect(() => {
    if (!effortOpen) {
      setEffortPanelPos(null);
      return undefined;
    }
    const place = () => {
      const trigger = effortTriggerRef.current;
      if (!trigger) return;
      const rect = trigger.getBoundingClientRect();
      const margin = 12;
      const left = Math.max(margin, Math.min(rect.left, window.innerWidth - margin - 120));
      setEffortPanelPos({
        left,
        bottom: Math.round(window.innerHeight - rect.top + 8),
        maxWidth: Math.max(200, window.innerWidth - left - margin),
      });
    };
    place();
    window.addEventListener('resize', place);
    window.addEventListener('scroll', place, true);
    return () => {
      window.removeEventListener('resize', place);
      window.removeEventListener('scroll', place, true);
    };
  }, [effortOpen]);


  const tierFromRatio = useCallback((ratio) => {
    const idx = Math.round(Math.min(1, Math.max(0, ratio)) * (EFFORT_TIERS.length - 1));
    return EFFORT_TIERS[idx].value;
  }, []);

  const ratioFromClientX = useCallback((clientX) => {
    const el = effortTrackRef.current;
    if (!el) return 0;
    const rect = el.getBoundingClientRect();
    if (!rect.width) return 0;
    return Math.min(1, Math.max(0, (clientX - rect.left) / rect.width));
  }, []);

  const handleTrackPointerDown = (event) => {
    event.preventDefault();
    setEffortDragging(true);
    try { event.currentTarget.setPointerCapture(event.pointerId); } catch { /* 忽略 */ }
    setEffortPreview(tierFromRatio(ratioFromClientX(event.clientX)));
  };

  const handleTrackPointerMove = (event) => {
    if (!effortDragging) return;
    setEffortPreview(tierFromRatio(ratioFromClientX(event.clientX)));
  };

  const endEffortDrag = (event) => {
    if (!effortDragging) return;
    setEffortDragging(false);
    try { event.currentTarget.releasePointerCapture(event.pointerId); } catch { /* 忽略 */ }
    const value = tierFromRatio(ratioFromClientX(event.clientX));
    setEffortPreview(null);
    handleEffort(value);          // 整个拖动过程只发这一次
  };

  const refreshModels = useCallback(async () => {
    try {
      const list = await fetchModels();
      if (Array.isArray(list)) setFetchedModels(list);
    } catch { /* 模型列表拿不到就只显示默认档，不影响发消息 */ }
  }, []);

  useEffect(() => { refreshModels(); }, [refreshModels]);

  // 档位的本地覆盖层：modelsProp 来自 App 的 state，而 App 不会因为我们改了
  // 档位而重新拉列表，所以只更新 fetchedModels 的话界面纹丝不动。这里用
  // overrides 兜住，保存成功才写入，失败不撒谎。
  const [effortOverrides, setEffortOverrides] = useState({});
  const baseModels = (Array.isArray(modelsProp) && modelsProp.length ? modelsProp : fetchedModels);
  const models = baseModels
    .filter((m) => m.available !== false)
    .map((m) => (
      Object.prototype.hasOwnProperty.call(effortOverrides, m.id)
        ? { ...m, reasoning_effort: effortOverrides[m.id] }
        : m
    ));
  const activeModelId = selectedModelProp
    || (() => { try { return window.localStorage.getItem('fairy.selectedModel') || ''; } catch { return ''; } })();
  const activeModel = models.find((m) => m.id === activeModelId);
  const savedEffort = activeModel ? (activeModel.reasoning_effort || '') : '';
  const currentEffort = effortPreview != null ? effortPreview : savedEffort;
  const effortLabel = EFFORT_TIERS.find((t) => t.value === currentEffort)?.label ?? '关';
  const effortIndex = EFFORT_TIERS.findIndex((t) => t.value === currentEffort);
  const effortRatio = EFFORT_TIERS.length > 1 ? effortIndex / (EFFORT_TIERS.length - 1) : 0;
  const EFFORT_SPAN = 100 / (EFFORT_TIERS.length - 1);
  const effortFillWidth = `calc(${effortRatio} * 100% + ${effortRatio > 0 ? 12 : 0}px)`;
  const effortThumbLeft = `calc(${effortRatio} * 100% + ${effortRatio > 0 ? 6 : 0}px)`;
  const effortDotLeft = (k) => `${k * EFFORT_SPAN}%`;
  const effortDotClass = (k) => `chat-effort__dot${k <= effortIndex ? ' passed' : ''}`;

  // 乐观更新：界面先到位，网络在后面追。
  // 之前是"等接口回来才改 UI"，弱网下拖完松手看着像没点上——那是把传输的
  // 延迟漏给了交互。现在立刻写入本地，失败再回滚并说明，不假装成功。
  const handleEffort = async (value) => {
    if (!activeModelId) return;
    const prevValue = savedEffort;
    setEffortOverrides((o) => ({ ...o, [activeModelId]: value }));
    try {
      const result = await updateModel(activeModelId, { reasoning_effort: value });
      if (result && result.ok === false) {
        setEffortOverrides((o) => ({ ...o, [activeModelId]: prevValue }));
        reportEffortError(`推理强度未保存：${result.error || '接口返回失败'}`);
      }
    } catch (e) {
      setEffortOverrides((o) => ({ ...o, [activeModelId]: prevValue }));
      reportEffortError(`推理强度未保存：${(e && e.message) || e}`);
    }
  };

  const [dragging, setDragging] = useState(false);
  const taRef = useRef(null);
  const pressTimerRef = useRef(null);
  const fileRef = useRef(null);
  const dragDepth = useRef(0);
  const sessionRef = useRef(sessionName);
  const sessionEstablishedByUploadRef = useRef(false);
  const uploadGenRef = useRef(0);

  useEffect(() => {
    const previous = sessionRef.current;
    sessionRef.current = sessionName;
    if (previous === sessionName) return;
    if (sessionEstablishedByUploadRef.current) {
      sessionEstablishedByUploadRef.current = false;
      return;
    }
    uploadGenRef.current += 1;
    setAttachments([]);
    setUploading(false);
    setDragging(false);
    dragDepth.current = 0;
  }, [sessionName]);

  const autoResize = () => {
    const ta = taRef.current;
    if (!ta) return;
    ta.style.height = 'auto';
    ta.style.height = Math.min(ta.scrollHeight, 200) + 'px';
  };

  // Picker and drag-and-drop share this path; only successful uploads are
  // listed. A brand new session is named first so the files land in that
  // session's upload folder instead of a timestamp fallback.
  const uploadFiles = async list => {
    // 只挡并发上传，不挡「思考中」：附件是本地待发状态，
    // agent 在跑的时候照样应该能先把图/文件攒进输入框。
    if (uploading) return;
    const picked = Array.from(list || []).filter(Boolean);
    if (!picked.length) return;
    const uploadGen = uploadGenRef.current;
    setUploading(true);
    let sess = sessionName;
    if (!sess && onEnsureSession) {
      try {
        sessionEstablishedByUploadRef.current = true;
        sess = await onEnsureSession();
      } catch {
        sessionEstablishedByUploadRef.current = false;
        sess = sessionName;
      }
    }
    for (const f of picked) {
      try {
        const res = await uploadFile(f, sess);
        if (uploadGenRef.current !== uploadGen) break;
        setAttachments(prev => [...prev, { name: res.name, path: res.path, size: res.size }]);
      } catch (err) {
        window.alert('上传失败：' + (err && err.message ? err.message : err));
      }
    }
    if (uploadGenRef.current === uploadGen) setUploading(false);
  };

  useImperativeHandle(ref, () => ({
    addFiles: files => {
      uploadFiles(files);
    },
  }), [loading, uploading, sessionName, onEnsureSession]);

  const handlePickFiles = async event => {
    const picked = Array.from(event.target.files || []);
    event.target.value = '';
    await uploadFiles(picked);
  };

  // dragDepth cancels the enter/leave pairs fired by child elements.
  const isFileDrag = event => {
    const dt = event.dataTransfer;
    if (!dt) return false;
    const types = Array.from(dt.types || []).map(type => String(type).toLowerCase());
    if (types.indexOf('files') >= 0) return true;
    return Array.from(dt.items || []).some(item => item && item.kind === 'file');
  };
  // 注意：拖拽这三个处理函数不能拿 loading 当门禁。
  // 发消息后 loading 变 true，会同时导致三件事：dragenter 不再 setDragging(true)
  // （提示不显示）、dragover 不再 preventDefault（浏览器判为非法放置目标、显示禁止光标）、
  // drop 被静默 return（松手什么也不发生）——正是"开始会话后拖拽彻底没反应"的成因。
  // uploadFiles 的注释已写明附件要在 agent 思考时也能攒进输入框，故这里只挡并发上传。
  const handleDragEnter = event => {
    if (uploading || !isFileDrag(event)) return;
    event.preventDefault();
    dragDepth.current += 1;
    setDragging(true);
  };
  const handleDragOver = event => {
    if (uploading || !isFileDrag(event)) return;
    event.preventDefault();
    if (event.dataTransfer) event.dataTransfer.dropEffect = 'copy';
  };
  const handleDragLeave = event => {
    event.preventDefault();
    dragDepth.current = Math.max(0, dragDepth.current - 1);
    if (dragDepth.current === 0) setDragging(false);
  };
  const handleDrop = event => {
    event.preventDefault();
    dragDepth.current = 0;
    setDragging(false);
    if (uploading) return;
    const dropped = event.dataTransfer ? Array.from(event.dataTransfer.files || []) : [];
    if (dropped.length) uploadFiles(dropped);
  };

  // 粘贴图片：截图工具和浏览器「复制图片」会把图片放进剪贴板的文件项里，
  // 而 textarea 默认只接收文字，所以这里把图片项转成 File 复用拖拽那条上传通道。
  const handlePaste = event => {
    if (uploading) return;
    const dt = event.clipboardData;
    if (!dt) return;
    const picked = [];
    // items 能按 MIME 精确判断；files 作为部分浏览器的兜底。
    for (const item of Array.from(dt.items || [])) {
      if (!item || item.kind !== 'file') continue;
      if (!/^image\//i.test(item.type || '')) continue;
      const file = item.getAsFile();
      if (file) picked.push(file);
    }
    if (!picked.length) {
      for (const file of Array.from(dt.files || [])) {
        if (/^image\//i.test(file.type || '')) picked.push(file);
      }
    }
    if (!picked.length) return; // 不是图片，交给浏览器默认粘贴文字
    event.preventDefault();
    // 截图工具给出的文件常常没有文件名，补一个可读的默认名。
    const named = picked.map(file => (file.name
      ? file
      : new File([file], `pasted-${Date.now()}.${(file.type.split('/')[1] || 'png').replace('jpeg', 'jpg')}`, { type: file.type })));
    uploadFiles(named);
  };
  const removeAttachment = index => setAttachments(prev => prev.filter((_, i) => i !== index));

  const handleSubmit = () => {
    const msg = stripInternalSummaryBlocks(text);
    if ((!msg && !attachments.length) || uploading) return;
    // Attachments alone are a valid turn: give the agent something to act on.
    const payload = msg || '请分析我上传的文件。';
    const files = attachments.map(a => ({ path: a.path }));
    setText('');
    setAttachments([]);
    if (taRef.current) taRef.current.style.height = 'auto';
    // Loading state is the responsibility of the parent App: App.send decides
    // whether to spawn a fresh agent (idle) or inject into the running one
    // (loading). We just forward.
    onSend(payload, files);
  };
  const handleKeyDown = e => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleSubmit();
    } else if (e.key === 'Escape' && loading && onAbort) {
      e.preventDefault();
      if (e.shiftKey) {
        // Shift+Esc = hard cancel (abandons in-flight work).
        onAbort({ hard: true });
      } else {
        onAbort({ hard: false });
      }
    }
  };
  const beginStopPress = () => {
    setHardCancelArmed(false);
    if (pressTimerRef.current) clearTimeout(pressTimerRef.current);
    pressTimerRef.current = setTimeout(() => setHardCancelArmed(true), 500);
  };
  const endStopPress = () => {
    if (pressTimerRef.current) clearTimeout(pressTimerRef.current);
    pressTimerRef.current = null;
  };
  const clickStop = () => {
    if (!onAbort) return;
    onAbort({ hard: hardCancelArmed });
    setHardCancelArmed(false);
  };
  return (
    <div className="input-row">
      <div
        className={`chat-input-box${dragging ? ' dragging' : ''}`}
        onPaste={handlePaste}
        onDragEnter={handleDragEnter}
        onDragOver={handleDragOver}
        onDragLeave={handleDragLeave}
        onDrop={handleDrop}
      >
        {dragging && <div className="chat-drop-hint">松开即可上传文件</div>}
        {attachments.length > 0 && (
          <div className="chat-attach-list">
            {attachments.map((a, i) => (
              <span className="chat-attach-chip" key={(a.path || a.name) + i}>
                <span className="chat-attach-icon" aria-hidden="true">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"
                    strokeLinecap="round" strokeLinejoin="round">
                    <path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z" />
                    <path d="M14 2v6h6" />
                  </svg>
                  <span className="chat-attach-ext">{fileExtension(a.name)}</span>
                </span>
                <span className="chat-attach-name" title={a.path}>{a.name}</span>
                <button type="button" className="chat-attach-x" onClick={() => removeAttachment(i)}
                  title="移除附件" aria-label="移除附件">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
                    strokeLinecap="round" aria-hidden="true">
                    <path d="M6 6l12 12M18 6L6 18" />
                  </svg>
                </button>
              </span>
            ))}
          </div>
        )}
        <textarea
          id="input"
          ref={taRef}
          value={text}
          onChange={e => { setText(e.target.value); autoResize(); }}
          onKeyDown={handleKeyDown}
          placeholder={loading ? 'Agent仍在生成，输入内容将插入当前对话…' : '输入消息…'}
          rows={1}
        />
        <div className="chat-input-footer">
          <div className="chat-input-footer-left">
            <button
              type="button"
              className="attach-btn"
              onClick={() => fileRef.current && fileRef.current.click()}
              // 与拖拽/粘贴统一：只挡并发上传，不挡「思考中」。
              // 附件是本地待发状态，agent 在跑的时候也该能先攒进输入框；
              // 之前这里多挂了一个 loading，导致思考中点「上传文件」没反应，
              // 而拖同一时刻却能用，行为自相矛盾。
              disabled={uploading}
              title="上传文件"
              aria-label="上传文件"
            >
              {uploading ? '···' : (
                <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor"
                  strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                  <path d="M21.44 11.05l-9.19 9.19a6 6 0 0 1-8.49-8.49l9.19-9.19a4 4 0 0 1 5.66 5.66l-9.2 9.19a2 2 0 0 1-2.83-2.83l8.49-8.48" />
                </svg>
              )}
            </button>
            <input ref={fileRef} type="file" multiple style={{ display: 'none' }} onChange={handlePickFiles} />
            <span className="chat-input-hint">
              {loading
                ? (hardCancelArmed
                    ? '松开 = 立即中断并放弃当前回复'
                    : 'Enter 插入 · Shift+Enter 换行 · Esc 软停止 · Shift+Esc 强停 · 长按 Stop = 强停')
                : 'Enter 发送 · Shift+Enter 换行'}
            </span>

          </div>
          <div className="chat-input-actions">
            <div className="chat-model-effort">
              <button
                ref={effortTriggerRef}
                type="button"
                className={`chat-model-effort__trigger${effortOpen ? ' open' : ''}`}
                aria-haspopup="dialog"
                aria-expanded={effortOpen}
                title="模型与推理强度"
                onClick={() => setEffortOpen((v) => !v)}
              >
                <span className="chat-model-effort__body">
                  <span className="chat-model-effort__name">
                    {activeModel ? (activeModel.display || activeModel.id) : '选择模型'}
                  </span>
                  <span className={`chat-model-effort__tier${currentEffort ? ' on' : ''}`}>
                    {currentEffort ? EFFORT_TIERS[effortIndex].label : '未设置'}
                  </span>
                </span>
                <span className="chat-model-effort__caret">›</span>
              </button>
              {effortOpen && createPortal(
                <>
                  <div className="chat-effort__scrim" onClick={() => setEffortOpen(false)} />
                  <div
                    className="chat-model-effort__panel"
                    role="dialog"
                    aria-label="模型与推理强度"
                    style={effortPanelPos ? {
                      position: 'fixed',
                      left: effortPanelPos.left,
                      bottom: effortPanelPos.bottom,
                      maxWidth: effortPanelPos.maxWidth,
                    } : { position: 'fixed', left: -9999, bottom: 0, opacity: 0 }}
                  >
                    <div className="chat-effort__head">
                      <div className={`chat-effort__big${currentEffort ? ' on' : ''}`}>
                        {effortLabel}
                      </div>
                      <code className="chat-effort__state">
                        reasoning_effort = {currentEffort || 'off'}
                      </code>
                    </div>
                    <div className="chat-effort__wrap">
                      <div
                        ref={effortTrackRef}
                        className={`chat-effort__slider${effortDragging ? ' dragging' : ''}`}
                        role="slider"
                        tabIndex={0}
                        aria-label="推理强度"
                        aria-valuemin={0}
                        aria-valuemax={EFFORT_TIERS.length - 1}
                        aria-valuenow={effortIndex}
                        aria-valuetext={EFFORT_TIERS[effortIndex]?.label || '关闭'}
                        onPointerDown={handleTrackPointerDown}
                        onPointerMove={handleTrackPointerMove}
                        onPointerUp={endEffortDrag}
                        onPointerCancel={endEffortDrag}
                        onKeyDown={(e) => {
                          if (e.key === 'ArrowLeft' || e.key === 'ArrowDown') {
                            e.preventDefault();
                            const next = EFFORT_TIERS[Math.max(0, effortIndex - 1)];
                            if (next) handleEffort(next.value);
                          } else if (e.key === 'ArrowRight' || e.key === 'ArrowUp') {
                            e.preventDefault();
                            const next = EFFORT_TIERS[Math.min(EFFORT_TIERS.length - 1, effortIndex + 1)];
                            if (next) handleEffort(next.value);
                          }
                        }}
                      >
                        <span
                          className="chat-effort__fill"
                          style={{ width: `calc(${effortRatio} * 100% + ${effortRatio > 0 ? 12 : 0}px)` }}
                        />
                        <span className="chat-effort__fill" style={{ width: effortFillWidth }} />
                        {EFFORT_TIERS.map((t, k) => (
                          <span
                            key={t.value}
                            className={effortDotClass(k)}
                            style={{ left: effortDotLeft(k) }}
                          />
                        ))}
                        <span className="chat-effort__thumb" style={{ left: effortThumbLeft }} />
                      </div>
                      <div className="chat-effort__ticks">
                        {EFFORT_TIERS.map((t) => (
                          <button
                            key={t.value}
                            type="button"
                            className={`chat-effort__tick${currentEffort === t.value ? ' on' : ''}`}
                            onClick={() => handleEffort(t.value)}
                          >
                            {t.label}
                          </button>
                        ))}
                      </div>
                    </div>
                    {effortNotice && <div className="chat-effort__warn">{effortNotice}</div>}
                    <p className="chat-effort__note">按模型分别保存；带工具时网关只接受 low / medium / high。</p>
                  </div>
                </>,
                document.body,
              )}
            </div>
            {loading ? (
              <>
                <button
                  className="send-btn inject"
                  onClick={handleSubmit}
                  disabled={(!text.trim() && attachments.length === 0) || uploading}
                  title="插入到当前对话"
                  aria-label="插入"
                >
                  <svg viewBox="0 0 24 24" width="18" height="18">
                    <path d="M12 5v14M5 12h14" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" />
                  </svg>
                </button>
                <button
                  className={`send-btn stop${hardCancelArmed ? ' armed' : ''}`}
                  onMouseDown={beginStopPress}
                  onMouseUp={endStopPress}
                  onMouseLeave={endStopPress}
                  onTouchStart={beginStopPress}
                  onTouchEnd={endStopPress}
                  onClick={clickStop}
                  title={hardCancelArmed ? '松开 = 硬中断（放弃当前回复）' : '软停止（长按 = 硬中断）'}
                  aria-label="停止"
                >
                  <svg viewBox="0 0 24 24" width="16" height="16"><rect x="6" y="6" width="12" height="12" rx="2" fill="currentColor"/></svg>
                </button>
              </>
            ) : (
              <button
                className="send-btn"
                onClick={handleSubmit}
                disabled={(!text.trim() && attachments.length === 0) || uploading}
                title="发送"
                aria-label="发送"
              >
                <svg viewBox="0 0 24 24" width="18" height="18"><path d="M12 4l-8 8h5v8h6v-8h5z" fill="currentColor"/></svg>
              </button>
            )}
          </div>
        </div>
      </div>
    </div>
  );
});

export default InputBar;
