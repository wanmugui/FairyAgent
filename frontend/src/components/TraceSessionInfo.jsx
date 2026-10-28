import TraceDeckScore from './TraceDeckScore';

function fmtTok(v) { return v != null ? Number(v).toLocaleString() : ''; }

// 对齐 8800 分析页：得分 pill + 参考答案 / 模型答案折叠块 + 主线程统计
export default function TraceSessionInfo({ data, onAnalyze, aiBusy, sessionName }) {
  const score = data && data.score;
  const ref = String((data && data.reference) || '').trim();
  const ans = String((data && data.answer) || '').trim();

  return (
    <div className="trace-session-info">
      {(score || ref || ans) && (
        <div className="tsi-block">
          {score && (
            <div className="tsi-score">
              <span className={'tsi-pill ' + (score.score >= 80 ? 'ok' : score.score >= 50 ? 'warn' : 'bad')}>
                得分 {score.score} · {score.verdict || ''}
              </span>
              {score.reason && <span className="tsi-reason">{score.reason}</span>}
            </div>
          )}
          {ref && (
            <details className="tsi-details"><summary>参考答案</summary>
              <pre className="tsi-text">{ref}</pre></details>
          )}
          {ans && (
            <details className="tsi-details"><summary>模型答案</summary>
              <pre className="tsi-text">{ans}</pre></details>
          )}
        </div>
      )}

      {data && data.kind !== 'da' && data.deck && (data.deck.htmls + data.deck.pngs) > 0 && (
        <TraceDeckScore data={data} sessionName={sessionName} />
      )}

      <div className="tsi-actions">
        {data.preview_url && (
          <a className="tsi-ai-btn" href={data.preview_url} target="_blank" rel="noopener"
            style={{ textDecoration: 'none' }}>▶ 预览 deck</a>
        )}
        <button className="tsi-ai-btn" onClick={onAnalyze} disabled={aiBusy}>
          {aiBusy ? '分析中…' : 'AI 分析'}
        </button>
      </div>
    </div>
  );
}