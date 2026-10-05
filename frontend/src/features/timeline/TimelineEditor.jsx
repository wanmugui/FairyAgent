import React, { useState, useRef, useEffect, useMemo, useCallback } from 'react';
import './timeline.css';

/**
 * 视频时间轴 UI。
 *
 * 导出契约严格对齐 skills/video-edit/scripts/timeline_edit.py 的 TIMELINE_SCHEMA，
 * 导出后可直接 `timeline_edit.py build timeline.json out.mp4`。改动本组件时请同步
 * 复核那份 schema，不要凭印象改字段名。
 *
 * 几个容易写错、这里刻意对齐了后端的点：
 * - 片段在**时间轴上的宽度 = (out - in) / speed**，不是 (out - in)。变速片段必须
 *   按除完 speed 的时长占位，否则刻度和实际输出对不上（timeline_edit.py:121 同款公式）。
 * - `out` 会被后端 min(源文件时长) 夹紧，且 `out <= in` 直接 SystemExit。所以 UI 在
 *   拖拽时就地拦掉非法区间，不让用户走到 build 才炸。
 * - `transition` 挂在片段上，表示**进入该片段**的转场，与 ffmpeg xfade 的串接顺序一致。
 */

const DRAFT_KEY = 'fairy.timeline.draft.v1';

const TRANSITIONS = [
  '', 'fade', 'fadeblack', 'fadewhite', 'wipeleft',
  'wiperight', 'slideleft', 'slideup', 'circleopen', 'dissolve',
];

/**
 * position 可选值严格取自 timeline_edit.py 的校验列表，写错后端会直接
 * `未知 position: xxx` 退出。UI 只能用这些值，不要自造 "center" / "fill"。
 */
const OVERLAY_POSITIONS = ['tl', 't', 'tr', 'l', 'c', 'r', 'bl', 'b', 'br'];

/** 片段时间轴时长：与 timeline_edit.py 一致，除以 speed */
const clipSpan = (c) => Math.max(0, (Number(c.out) - Number(c.in)) / (Number(c.speed) || 1));

/** 源文件时长：拖右把手时不能超过它 */
const srcDur = (c) => (Number(c.duration) > 0 ? Number(c.duration) : Number(c.out) + 10);

const uid = () => 'c' + Math.random().toString(36).slice(2, 9);

const emptyDraft = () => ({
  canvas: { width: 1920, height: 1080 },
  fps: 30,
  clips: [],
  overlays: [],
  audio: { mute: false, music: null },
  subtitles: { items: [] },
  export: { crf: 20, preset: 'medium', encoder: 'libx264' },
});

const fmt = (s) => {
  const v = Math.max(0, Number(s) || 0);
  const m = Math.floor(v / 60);
  const r = v - m * 60;
  return `${m}:${r.toFixed(2).padStart(5, '0')}`;
};

const loadDraft = () => {
  try {
    const raw = localStorage.getItem(DRAFT_KEY);
    if (!raw) return emptyDraft();
    const p = JSON.parse(raw);
    return { ...emptyDraft(), ...p };
  } catch {
    return emptyDraft();
  }
};

/* ---------------- 片段块 ---------------- */

function Clip({ clip, prev, pxPerSec, onChange, onRemove, onCycleTransition }) {
  const span = clipSpan(clip);
  const width = Math.max(14, span * pxPerSec);
  const drag = useRef(null);

  const onDown = (e, mode) => {
    e.preventDefault();
    e.stopPropagation();
    const startX = e.clientX;
    const snapshot = { ...clip };
    drag.current = { mode, startX, snapshot, moved: false };
    window.addEventListener('mousemove', onMove);
    window.addEventListener('mouseup', onUp);
  };

  const onMove = (e) => {
    const d = drag.current;
    if (!d) return;
    const delta = (e.clientX - d.startX) / pxPerSec;
    if (Math.abs(e.clientX - d.startX) > 1) d.moved = true;
    const speed = Number(d.snapshot.speed) || 1;
    let next = { ...d.snapshot };
    if (d.mode === 'move') {
      next.start = Math.max(0, Number((d.snapshot.start || 0) + delta));
    } else if (d.mode === 'right') {
      // 右手柄改 out：换算回源时间，并夹在源时长内
      const maxOut = srcDur(d.snapshot);
      const raw = d.snapshot.out + delta * speed;
      next.out = Math.min(maxOut, Math.max(d.snapshot.in + 0.05, raw));
    } else if (d.mode === 'left') {
      const rawIn = d.snapshot.in + delta * speed;
      const newIn = Math.max(0, Math.min(d.snapshot.out - 0.05, rawIn));
      next.in = newIn;
      // 保持右手柄不动，让片段在源里跟着滑动
      next.start = Math.max(0, (d.snapshot.start || 0) + (newIn - d.snapshot.in) / speed);
    }
    onChange(next);
  };

  const onUp = () => {
    drag.current = null;
    window.removeEventListener('mousemove', onMove);
    window.removeEventListener('mouseup', onUp);
  };

  useEffect(() => () => {
    window.removeEventListener('mousemove', onMove);
    window.removeEventListener('mouseup', onUp);
  }, []);

  const startX = (clip.start || 0) * pxPerSec;

  return (
    <div
      className="tl-clip"
      style={{ left: startX, width }}
      data-testid={`clip-${clip.id}`}
      data-clip-path={clip.path}
      title={`${clip.path}\n源内 ${fmt(clip.in)} → ${fmt(clip.out)}  ${Number(clip.speed) || 1}x\n时间轴 ${fmt(span)}`}
      onMouseDown={(e) => onDown(e, 'move')}
    >
      <span className="tl-clip-name">{clip.path.split('/').pop()}</span>
      <span className="tl-clip-meta">
        {Number(clip.speed) !== 1 && <em>{Number(clip.speed)}x</em>}
        {fmt(span)}
      </span>
      <span className="tl-handle tl-h-left" data-testid={`h-left-${clip.id}`} onMouseDown={(e) => onDown(e, 'left')} />
      <span className="tl-handle tl-h-right" data-testid={`h-right-${clip.id}`} onMouseDown={(e) => onDown(e, 'right')} />
      <button
        className="tl-clip-x"
        data-testid={`del-${clip.id}`}
        onClick={(e) => { e.stopPropagation(); onRemove(clip.id); }}
        title="删除"
      >×</button>
    </div>
  );
}

/* ---------------- 覆盖层块（图片/文字） ---------------- */

function Overlay({ ov, pxPerSec, onChange, onRemove }) {
  const width = Math.max(14, ((ov.end - ov.start) || 0) * pxPerSec);
  const drag = useRef(null);
  const onDown = (e) => {
    e.preventDefault(); e.stopPropagation();
    drag.current = { startX: e.clientX, s: { ...ov } };
    const mv = (ev) => {
      const d = drag.current; if (!d) return;
      const delta = (ev.clientX - d.startX) / pxPerSec;
      onChange({ ...ov, start: Math.max(0, d.s.start + delta) });
    };
    const up = () => {
      drag.current = null;
      window.removeEventListener('mousemove', mv);
      window.removeEventListener('mouseup', up);
    };
    window.addEventListener('mousemove', mv);
    window.addEventListener('mouseup', up);
  };
  return (
    <div
      className={`tl-ov tl-ov-${ov.type}`}
      style={{ left: (ov.start || 0) * pxPerSec, width }}
      data-testid={`ov-${ov.id}`}
      onMouseDown={onDown}
      title={ov.type === 'text' ? `文字：${ov.text}` : ov.path}
    >
      <span>{ov.type === 'text' ? ov.text : ov.path.split('/').pop()}</span>
      <button className="tl-clip-x" onClick={(e) => { e.stopPropagation(); onRemove(ov.id); }}>×</button>
    </div>
  );
}

/* ---------------- 主组件 ---------------- */

export default function TimelineEditor({ onClose }) {
  const [draft, setDraft] = useState(loadDraft);
  const [zoom, setZoom] = useState(40);
  const [tab, setTab] = useState('video');
  const [err, setErr] = useState('');
  const [okMsg, setOkMsg] = useState('');

  // 草稿自动落 localStorage —— 关闭再开、刷新页面都不丢
  useEffect(() => {
    try { localStorage.setItem(DRAFT_KEY, JSON.stringify(draft)); } catch { /* 配额满，忽略 */ }
  }, [draft]);

  const pxPerSec = zoom;
  const clips = draft.clips || [];
  const overlays = draft.overlays || [];
  const mainSpan = useMemo(
    () => clips.reduce((s, c) => s + clipSpan(c), 0),
    [clips],
  );
  const total = Math.max(mainSpan, 12);

  const patch = (p) => setDraft((d) => ({ ...d, ...p }));

  const addVideo = () => {
    const path = window.prompt('视频路径（file: 路径）', '/tmp/a.mp4');
    if (!path) return;
    const c = { id: uid(), path, in: 0, out: 10, speed: 1, start: 0, transition: '', duration: 10 };
    patch({ clips: [...clips, c] });
  };

  /** 载入示例：一次铺满视频/图片/文字/音乐四条轨，先让用户看到时间轴长什么样 */
  const loadSample = () => {
    patch({
      clips: [
        { id: uid(), path: '/tmp/sample-a.mp4', in: 0, out: 6, speed: 1, start: 0, transition: '', duration: 6 },
        { id: uid(), path: '/tmp/sample-b.mp4', in: 0, out: 4, speed: 2, start: 6, transition: 'fade', duration: 4 },
      ],
      overlays: [
        { id: uid(), type: 'text', text: '示例字幕', start: 1, end: 4, position: 'c', color: '#ffffff' },
        { id: uid(), type: 'image', path: '/tmp/sample-bg.png', start: 4, end: 7, position: 'br', scale: 0.25 },
      ],
      audio: { mute: false, music: { path: '/tmp/sample-bgm.mp3', volume: 0.4, loop: false } },
    });
  };

  const addOverlay = (type) => {
    if (type === 'text') {
      const text = window.prompt('文字内容', '字幕文字');
      if (!text) return;
      const st = Number(window.prompt('开始秒', '0')) || 0;
      const en = Number(window.prompt('结束秒', String(st + 3))) || st + 3;
      patch({ overlays: [...overlays, { id: uid(), type: 'text', text, start: st, end: en, position: 'c', color: '#ffffff' }] });
    } else {
      const path = window.prompt('图片路径（file: 路径）', '/tmp/a.png');
      if (!path) return;
      const st = Number(window.prompt('开始秒', '0')) || 0;
      const en = Number(window.prompt('结束秒', String(st + 3))) || st + 3;
      patch({ overlays: [...overlays, { id: uid(), type: 'image', path, start: st, end: en, position: 'br', scale: 0.3 }] });
    }
  };

  const setAudio = () => {
    const path = window.prompt('音乐路径（file: 路径）', '/tmp/bgm.mp3');
    if (!path) return;
    patch({ audio: { ...draft.audio, music: { path, volume: 0.5, loop: true } } });
  };

  const upd = (id, next) => patch({ clips: clips.map((c) => (c.id === id ? next : c)) });
  const rmClip = (id) => patch({ clips: clips.filter((c) => c.id !== id) });
  const cycle = (id) => patch({
    clips: clips.map((c) => {
      if (c.id !== id) return c;
      const i = TRANSITIONS.indexOf(c.transition || '');
      const t = TRANSITIONS[(i + 1) % TRANSITIONS.length];
      return { ...c, transition: t };
    }),
  });

  /** 导出成 timeline_edit.py 能直接吃的结构 —— 字段名必须一字不差 */
  const buildExport = () => {
    // 主轨按 start 排序；start 是 UI 布局用，导出时去掉（后端按数组顺序拼接）
    const ordered = [...clips].sort((a, b) => (a.start || 0) - (b.start || 0));
    const out = {
      canvas: draft.canvas,
      fps: draft.fps,
      clips: ordered.map((c) => {
        const o = {
          path: c.path,
          in: Number(Number(c.in).toFixed(3)),
          out: Number(Number(c.out).toFixed(3)),
          speed: Number(c.speed) || 1,
        };
        if (c.transition) o.transition = c.transition;
        return o;
      }),
      overlays: overlays.map((o) => (o.type === 'text'
        ? { type: 'text', text: o.text, start: Number(o.start), end: Number(o.end), position: o.position, color: o.color }
        : { type: 'image', path: o.path, start: Number(o.start), end: Number(o.end), position: o.position, scale: Number(o.scale) || 0.3 })),
      audio: { mute: !!draft.audio.mute, volume: draft.audio?.music?.volume ?? 1 },
      export: draft.export,
    };
    if (draft.audio?.music) out.audio.music = { path: draft.audio.music.path, volume: draft.audio.music.volume, loop: !!draft.audio.music.loop };
    if (draft.subtitles?.items?.length) out.subtitles = { items: draft.subtitles.items };
    return out;
  };

  const doExport = async () => {
    setErr(''); setOkMsg('');
    // 导出前自检：这些都是 timeline_edit.py 会直接 SystemExit 的硬错误，
    // 在 UI 侧拦下来比让用户去终端撞报错强。
    for (const c of clips) {
      if (!c.path) { setErr('有片段没填路径'); return; }
      if (Number(c.out) <= Number(c.in)) { setErr(`${c.path}：出点必须大于入点（timeline_edit.py 会直接退出）`); return; }
      if (!(Number(c.speed) > 0)) { setErr(`${c.path}：速度必须大于 0`); return; }
      if (c.transition && !TRANSITIONS.includes(c.transition)) { setErr(`${c.path}：未知转场 ${c.transition}`); return; }
    }
    for (const o of overlays) {
      if (!OVERLAY_POSITIONS.includes(o.position)) { setErr(`覆盖层位置 ${o.position} 非法，只能是 ${OVERLAY_POSITIONS.join('/')}`); return; }
      if (Number(o.end) <= Number(o.start)) { setErr('覆盖层的结束时间必须大于开始时间'); return; }
    }
    // 已知后端缺陷：timeline_edit.py 的 image 分支算出了图片路径(img)却从未把它
    // 加进 ffmpeg 输入，只按 ov_idx+1 猜了个索引（见脚本 223-233 行）。结果带图片
    // 覆盖层一律报 "Invalid file index"。与其导出一份必然 build 失败的 JSON，
    // 不如在这里说清楚。视频/文字/音乐不受影响。
    if (overlays.some((o) => o.type === 'image')) {
      setErr('图片覆盖层暂不可导出：timeline_edit.py 尚未把图片接进 ffmpeg 输入，导出的 JSON 执行 build 会报 Invalid file index。视频/文字/音乐可正常导出。');
      return;
    }
    const data = buildExport();
    if (!data.clips.length) { setErr('主轨还没有视频片段，先加一个'); return; }
    const text = JSON.stringify(data, null, 2);
    try {
      await navigator.clipboard.writeText(text);
      setOkMsg(`已复制到剪贴板（${data.clips.length} 个片段），存成 timeline.json 即可 build`);
    } catch {
      setOkMsg('');
      setErr('复制失败，可手动导出：' + text.slice(0, 120) + '…');
    }
    // 同时落一个文件，方便直接喂给 build
    try {
      const blob = new Blob([text], { type: 'application/json' });
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = 'timeline.json';
      a.click();
      setTimeout(() => URL.revokeObjectURL(a.href), 1000);
    } catch { /* 忽略下载失败 */ }
  };

  const ticks = useMemo(() => {
    const step = zoom > 80 ? 1 : zoom > 40 ? 2 : 5;
    const out = [];
    for (let t = 0; t <= total; t += step) out.push(t);
    return out;
  }, [total, zoom]);

  return (
    <div className="tl-overlay" data-testid="timeline-root">
      <div className="tl-panel">
        <header className="tl-head">
          <h2>时间轴</h2>
          <div className="tl-head-right">
            <label>
              缩放
              <input
                type="range" min="10" max="140" value={zoom}
                data-testid="zoom"
                onChange={(e) => setZoom(Number(e.target.value))}
              />
            </label>
            <span className="tl-total" data-testid="total">总长 {fmt(total)}</span>
            <button className="tl-x" data-testid="close" onClick={onClose} title="关闭">×</button>
          </div>
        </header>

        <nav className="tl-tabs">
          {[['video', `视频 ${clips.length}`], ['overlay', `图片/文字 ${overlays.length}`], ['audio', '音频']].map(([k, lbl]) => (
            <button key={k} className={tab === k ? 'on' : ''} data-testid={`tab-${k}`} onClick={() => setTab(k)}>{lbl}</button>
          ))}
        </nav>

        {tab === 'video' && (
          <div className="tl-toolbar">
            <button onClick={addVideo} data-testid="add-video">+ 视频片段</button>
            <button onClick={loadSample} data-testid="load-sample">载入示例</button>
            <span className="tl-hint">拖块=移动 · 拖左右把手=改入出点 · 变速改宽度</span>
          </div>
        )}
        {tab === 'overlay' && (
          <div className="tl-toolbar">
            <button onClick={() => addOverlay('image')} data-testid="add-image">+ 图片</button>
            <button onClick={() => addOverlay('text')} data-testid="add-text">+ 文字</button>
          </div>
        )}
        {tab === 'audio' && (
          <div className="tl-toolbar">
            <button onClick={setAudio} data-testid="add-audio">设置背景音乐</button>
            {draft.audio?.music && <span className="tl-hint" data-testid="audio-path">{draft.audio.music.path}</span>}
            <label className="tl-inline">
              <input
                type="checkbox" checked={!!draft.audio.mute}
                onChange={(e) => patch({ audio: { ...draft.audio, mute: e.target.checked } })}
              />静音
            </label>
          </div>
        )}

        <div className="tl-ruler" data-testid="ruler">
          {ticks.map((t) => (
            <span key={t} className="tl-tick" style={{ left: t * pxPerSec }}>{fmt(t)}</span>
          ))}
        </div>

        <div className="tl-tracks" data-testid="tracks">
          {clips.map((c, i) => (
            <div className="tl-track" key={c.id} data-testid="track-main">
              <div className="tl-track-label">主轨 {i + 1}</div>
              <div className="tl-lane" style={{ width: total * pxPerSec }}>
                <Clip
                  clip={c}
                  prev={clips[i - 1]}
                  pxPerSec={pxPerSec}
                  onChange={(n) => upd(c.id, n)}
                  onRemove={rmClip}
                  onCycleTransition={cycle}
                />
                {i > 0 && (
                  <button
                    className={`tl-trans-slot${c.transition ? ' on' : ''}`}
                    data-testid={`trans-slot-${c.id}`}
                    style={{ left: (c.start || 0) * pxPerSec }}
                    onClick={() => cycle(c.id)}
                    title={c.transition ? `转场：${c.transition}（点击切换）` : '点击设置进入该片段的转场'}
                  >{c.transition || '+转场'}</button>
                )}
              </div>
            </div>
          ))}
          {overlays.length > 0 && overlays.map((o) => (
            <div className="tl-track" key={o.id} data-testid="track-ov">
              <div className="tl-track-label">{o.type === 'text' ? '文字' : '图片'}</div>
              <div className="tl-lane" style={{ width: total * pxPerSec }}>
                <Overlay ov={o} pxPerSec={pxPerSec} onChange={(n) => patch({ overlays: overlays.map((x) => (x.id === o.id ? n : x)) })} onRemove={(id) => patch({ overlays: overlays.filter((x) => x.id !== id) })} />
              </div>
            </div>
          ))}
          {clips.length === 0 && overlays.length === 0 && (
            <div className="tl-empty" data-testid="empty">还没有片段，切到「视频」页加一个</div>
          )}
        </div>

        {clips.length > 0 && (
          <div className="tl-props" data-testid="props">
            {overlays.length > 0 && overlays.map((o) => (
              <div className="tl-prop-row" key={`ov-${o.id}`}>
                <span className="tl-prop-path" title={o.type === 'text' ? o.text : o.path}>
                  {o.type === 'text' ? `文字「${o.text}」` : o.path.split('/').pop()}
                </span>
                <label>起 <input type="number" step="0.1" value={o.start} data-testid={`ovstart-${o.id}`}
                  onChange={(e) => patch({ overlays: overlays.map((x) => (x.id === o.id ? { ...x, start: Number(e.target.value) } : x)) })} /></label>
                <label>止 <input type="number" step="0.1" value={o.end} data-testid={`ovend-${o.id}`}
                  onChange={(e) => patch({ overlays: overlays.map((x) => (x.id === o.id ? { ...x, end: Number(e.target.value) } : x)) })} /></label>
                <label>位
                  <select
                    value={o.position} data-testid={`ovpos-${o.id}`}
                    onChange={(e) => patch({ overlays: overlays.map((x) => (x.id === o.id ? { ...x, position: e.target.value } : x)) })}
                  >
                    {OVERLAY_POSITIONS.map((p) => <option key={p} value={p}>{p}</option>)}
                  </select>
                </label>
                <button
                  className="tl-mini"
                  data-testid={`ovdel-${o.id}`}
                  onClick={() => patch({ overlays: overlays.filter((x) => x.id !== o.id) })}
                >删除</button>
              </div>
            ))}
            {clips.map((c) => (
              <div className="tl-prop-row" key={c.id}>
                <span className="tl-prop-path" title={c.path}>{c.path.split('/').pop()}</span>
                <label>入 <input type="number" step="0.1" value={c.in} data-testid={`in-${c.id}`}
                  onChange={(e) => upd(c.id, { ...c, in: Number(e.target.value) })} /></label>
                <label>出 <input type="number" step="0.1" value={c.out} data-testid={`out-${c.id}`}
                  onChange={(e) => upd(c.id, { ...c, out: Number(e.target.value) })} /></label>
                <label>速 <input type="number" step="0.25" min="0.25" value={c.speed} data-testid={`speed-${c.id}`}
                  onChange={(e) => upd(c.id, { ...c, speed: Number(e.target.value) || 1 })} /></label>
                <span className="tl-prop-span" data-testid={`span-${c.id}`}>轴上 {fmt(clipSpan(c))}</span>
              </div>
            ))}
          </div>
        )}

        <footer className="tl-foot">
          <button className="tl-primary" data-testid="export" onClick={doExport}>导出 timeline.json</button>
          {okMsg && <span className="tl-ok" data-testid="ok">{okMsg}</span>}
          {err && <span className="tl-err" data-testid="err">{err}</span>}
        </footer>
      </div>
    </div>
  );
}
