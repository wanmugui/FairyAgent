import React, { forwardRef, useEffect, useImperativeHandle, useState, useRef } from 'react';
import { uploadFile } from '../api/chat';

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
  { onSend, loading, onAbort, sessionName, onEnsureSession },
  ref,
) {
  const [text, setText] = useState('');
  const [hardCancelArmed, setHardCancelArmed] = useState(false);
  const [attachments, setAttachments] = useState([]);
  const [uploading, setUploading] = useState(false);
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
  const handleDragEnter = event => {
    if (loading || !isFileDrag(event)) return;
    event.preventDefault();
    dragDepth.current += 1;
    setDragging(true);
  };
  const handleDragOver = event => {
    if (loading || !isFileDrag(event)) return;
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
    if (loading || uploading) return;
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
              disabled={loading || uploading}
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
