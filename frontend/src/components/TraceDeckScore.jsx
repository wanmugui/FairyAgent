import { useEffect, useState } from 'react';

// PPT 成品图像评分：逐页评分 + 总分，数据来自 /api/sessions/<name>/trace 的 deck_score
// （由 batch_eval/scripts/score_deck.py 产出 <deck>/deck_score.json，trace 轮询实时刷新）
const DIMS = [['content', '内容'], ['visual', '视觉'], ['layout', '版式']];

function scoreClass(score) {
  if (score == null) return 'info';
  if (score >= 85) return 'ok';
  if (score >= 70) return 'warn';
  return 'bad';
}

export default function TraceDeckScore({ data, sessionName }) {
  const score = (data && data.deck_score) || null;
  const deckRel = (data && data.deck_rel) || '';
  const [openPage, setOpenPage] = useState(null);
  const [pending, setPending] = useState(false);
  const [err, setErr] = useState('');

  const status = score && score.status;
  const running = status === 'running' || pending;

  useEffect(() => {
    if (pending && score && status && status !== 'running') setPending(false);
  }, [pending, score, status]);

  // POST 失败/超时兜底：15s 后放开按钮，避免一直卡在「评分中」
  useEffect(() => {
    if (!pending) return undefined;
    const t = setTimeout(() => setPending(false), 15000);
    return () => clearTimeout(t);
  }, [pending]);

  const run = async (force, pages) => {
    setErr('');
    setPending(true);
    try {
      const r = await fetch('/api/deck-score', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ session: sessionName, force: !!force, pages: pages || '' }),
      });
      const d = await r.json();
      if (d && d.error) { setErr(d.error); setPending(false); }
    } catch (e) {
      setErr(String(e));
      setPending(false);
    }
  };

  const pages = (score && Array.isArray(score.pages)) ? score.pages : [];
  const total = (score && score.total_pages) || 0;
  const scored = (score && score.scored_pages != null) ? score.scored_pages : pages.length;
  const overall = score && score.overall != null ? score.overall : null;
  const dims = (score && score.dimensions) || {};
  const detail = openPage == null ? null : pages.find((p) => p.page === openPage) || null;
  const basePath = deckRel ? '/api/ppt-decks/' + deckRel.split('/').map(encodeURIComponent).join('/') : '';

  return (
    <div className="tsi-block tsi-ds">
      <div className="tsi-ds-head">
        <span className="tsi-ds-name">PPT 成品评分</span>
        {overall != null && (
          <span className={'tsi-pill ' + scoreClass(overall)}>
            总分 {overall}{score.grade ? ' · ' + score.grade : ''}
          </span>
        )}
        <span className="tsi-ds-meta">
          {running
            ? '图像评分中 ' + scored + '/' + (total || '?')
            : overall != null
              ? '已评 ' + scored + (total ? '/' + total : '') + ' 页' + (score.judged_at ? ' · ' + score.judged_at : '')
              : '尚未评分（逐页渲染 + 视觉模型打分）'}
        </span>
        <button
          className="tsi-ai-btn tsi-ds-run"
          disabled={running || !deckRel}
          onClick={() => run(!!score)}
        >
          {running ? '评分中…' : overall != null ? '重新评分' : '开始评分'}
        </button>
      </div>

      {err && <div className="tsi-ds-err">{err}</div>}

      {overall != null && (
        <div className="tsi-ds-dims">
          {DIMS.map(([key, label]) => {
            const value = dims[key];
            return (
              <div className="tsi-ds-dim" key={key}>
                <span className="tsi-ds-dim-name">{label}</span>
                <span className="tsi-ds-dim-bar">
                  <i style={{ width: (value == null ? 0 : value) + '%' }} />
                </span>
                <span className="tsi-ds-dim-val">{value == null ? '-' : value}</span>
              </div>
            );
          })}
        </div>
      )}

      {pages.length > 0 && (
        <div className="tsi-ds-pages">
          {pages.map((p) => (
            <button
              key={p.page}
              className={'tsi-ds-chip ' + scoreClass(p.score) + (openPage === p.page ? ' open' : '')}
              title={'第 ' + p.page + ' 页 · ' + (p.verdict || '')}
              onClick={() => setOpenPage((v) => (v === p.page ? null : p.page))}
            >
              <span className="tsi-ds-chip-no">P{p.page}</span>
              <span className="tsi-ds-chip-val">{p.score}</span>
              {Array.isArray(p.issues) && p.issues.length > 0 && <span className="tsi-ds-chip-dot" />}
            </button>
          ))}
        </div>
      )}

      {detail && (
        <div className="tsi-ds-detail">
          {(detail.png_data || detail.png) && (
            <a className="tsi-ds-thumb"
              href={detail.png_data || (basePath + '/' + detail.png)}
              target="_blank" rel="noopener noreferrer">
              <img src={detail.png_data || (basePath + '/' + detail.png)}
                alt={'第 ' + detail.page + ' 页截图'} loading="lazy" />
            </a>
          )}
          <div className="tsi-ds-detail-body">
            <div className="tsi-ds-detail-head">
              <span className={'tsi-pill ' + scoreClass(detail.score)}>P{detail.page} · {detail.score}</span>
              {detail.verdict && <span className="tsi-ds-verdict">{detail.verdict}</span>}
            </div>
            {detail.reason && <p className="tsi-ds-reason">{detail.reason}</p>}
            {detail.dimensions && (
              <div className="tsi-ds-page-dims">
                {DIMS.map(([key, label]) => (detail.dimensions[key] != null
                  ? <span key={key}>{label} <b>{detail.dimensions[key]}</b></span>
                  : null))}
              </div>
            )}
            {Array.isArray(detail.issues) && detail.issues.length > 0 && (
              <ul className="tsi-ds-issues">
                {detail.issues.map((t, i) => <li key={i}>{t}</li>)}
              </ul>
            )}
          </div>
        </div>
      )}

      {score && Array.isArray(score.errors) && score.errors.length > 0 && (
        <div className="tsi-ds-err tsi-ds-err-row">
          <span>{score.errors.length} 页评分失败：{score.errors.map((e) => 'P' + e.page).join('、')}</span>
          <button
            className="tsi-ai-btn tsi-ds-retry"
            disabled={running || !deckRel}
            title="只对失败页重新渲染 + 重新判分，已评页面保持不变"
            onClick={() => run(false, score.errors.map((e) => e.page))}
          >
            重试失败页 ({score.errors.length})
          </button>
        </div>
      )}
    </div>
  );
}
