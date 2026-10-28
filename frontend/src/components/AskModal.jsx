import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { normalizeAskQuestions } from '../utils/askUser';

const optionColors = ['#E8F4FD', '#FFF3E0', '#E8F5E9', '#FCE4EC', '#F3E5F5'];

export default function AskModal({ questions, askType, autoSelectAfterSeconds, onComplete, onSkipAll, onRenew }) {
  const normalizedQuestions = useMemo(
    () => normalizeAskQuestions({ questions }),
    [questions],
  );
  const [currentIdx, setCurrentIdx] = useState(0);
  const [answers, setAnswers] = useState({});
  const [freeText, setFreeText] = useState('');
  // The backend owns the deadline (agent/internal/biz/tool/builtin/ask_user_wait.go)
  // and publishes it on the waiting_user_input event. Keeping a second timer
  // here was the double-countdown hazard: two clocks racing to resolve the
  // same ask, with no way to tell which one answered. We only render what the
  // backend told us.
  const deadlineSeconds = Number.isFinite(autoSelectAfterSeconds) && autoSelectAfterSeconds > 0
    ? Math.ceil(autoSelectAfterSeconds)
    : null;
  const [secondsLeft, setSecondsLeft] = useState(deadlineSeconds ?? null);
  const submittedRef = useRef(false);
  const lastRenewAtRef = useRef(0);

  const maxIdx = Math.max(normalizedQuestions.length - 1, 0);
  const safeIdx = Math.min(currentIdx, maxIdx);
  const q = normalizedQuestions[safeIdx] || null;
  const isLast = normalizedQuestions.length === 0 || safeIdx >= normalizedQuestions.length - 1;
  const selected = q ? (answers[q.id] || []) : [];
  const isMulti = q?.multi_select === true;

  const finishOnce = useCallback((payload) => {
    if (submittedRef.current) return;
    submittedRef.current = true;
    onComplete?.(payload);
  }, [onComplete]);

  const skipOnce = useCallback(() => {
    if (submittedRef.current) return;
    submittedRef.current = true;
    onSkipAll?.();
  }, [onSkipAll]);

  const renewDeadline = useCallback(() => {
    if (submittedRef.current) return;
    setSecondsLeft(deadlineSeconds);
    if (!onRenew) return;
    const now = Date.now();
    if (now - lastRenewAtRef.current < 500) return;
    lastRenewAtRef.current = now;
    Promise.resolve(onRenew())
      .then(result => {
        const remaining = Number(result?.remaining_seconds);
        if (Number.isFinite(remaining) && remaining > 0) {
          setSecondsLeft(Math.ceil(remaining));
        }
      })
      .catch(() => {
        // The backend deadline remains authoritative. A failed renewal will
        // naturally resolve on its original deadline instead of hanging.
      });
  }, [deadlineSeconds, onRenew]);

  const advanceWithAnswers = useCallback((nextAnswers) => {
    if (isLast) {
      setFreeText('');
      finishOnce(nextAnswers);
      return;
    }
    setAnswers(nextAnswers);
    setCurrentIdx(index => Math.min(index + 1, maxIdx));
    setFreeText('');
    setSecondsLeft(deadlineSeconds);
  }, [deadlineSeconds, finishOnce, isLast, maxIdx]);

  useEffect(() => {
    submittedRef.current = false;
    setCurrentIdx(0);
    setAnswers({});
    setFreeText('');
    setSecondsLeft(deadlineSeconds);
  }, [deadlineSeconds, normalizedQuestions, askType]);

  const toggleOption = useCallback((opt) => {
    if (!q || submittedRef.current) return;
    const label = opt?.label ?? opt;
    if (label == null) return;
    renewDeadline();
    const previous = answers[q.id] || [];
    if (isMulti) {
      const next = previous.includes(label)
        ? previous.filter(item => item !== label)
        : [...previous, label];
      setAnswers({ ...answers, [q.id]: next });
      return;
    }
    const nextAnswers = { ...answers, [q.id]: [label] };
    if (opt?.description) nextAnswers[`${q.id}_desc`] = opt.description;
    setAnswers(nextAnswers);
    advanceWithAnswers(nextAnswers);
  }, [advanceWithAnswers, answers, deadlineSeconds, isMulti, q]);

  const handleNext = useCallback(() => {
    if (!q || submittedRef.current) return;
    const nextAnswers = { ...answers };
    if (freeText.trim()) nextAnswers[`${q.id}_free_text`] = freeText.trim();
    if (!isLast) renewDeadline();
    advanceWithAnswers(nextAnswers);
  }, [advanceWithAnswers, answers, freeText, isLast, q, renewDeadline]);

  // Display-only countdown. When it hits zero the backend resolves the ask
  // and pushes the answer; we deliberately do NOT auto-submit here, otherwise
  // the frontend and backend would both be racing to pick an option.
  useEffect(() => {
    if (submittedRef.current || secondsLeft == null) return undefined;
    if (secondsLeft <= 0) return undefined;
    const timer = window.setTimeout(() => setSecondsLeft(value => (value == null ? null : value - 1)), 1000);
    return () => window.clearTimeout(timer);
  }, [secondsLeft]);

  if (!q) {
    return (
      <div className="ask-modal-overlay">
        <div className="ask-modal-card confirm-card">
          <div className="ask-modal-header">
            <div className="ask-modal-title">确认操作</div>
            <span className="ask-modal-progress">确认</span>
          </div>
          <div className="ask-modal-confirm-body">
            <p>是否继续执行？</p>
          </div>
          <div className="ask-modal-footer">
            <span className="ask-modal-skip" onClick={skipOnce}>跳过</span>
            <button className="ask-modal-next" onClick={() => finishOnce({ confirmed: true, askType })}>
              确认
            </button>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="ask-modal-overlay">
      <div className="ask-modal-card">
        <div className="ask-modal-header">
          <div className="ask-modal-title">{q.question || q.title || ''}</div>
          {isMulti && <span className="ask-modal-tag">多选</span>}
          <span className="ask-modal-progress">{safeIdx + 1} / {normalizedQuestions.length}</span>
        </div>

        <div className="ask-modal-options">
          {q.options.map((opt, oi) => {
            const label = opt?.label ?? opt;
            const desc = opt?.description || '';
            const isSel = selected.includes(label);
            return (
              <div
                key={oi}
                className={`ask-option-card ${isSel ? 'selected' : ''}`}
                style={{ '--card-color': optionColors[oi % optionColors.length] }}
                onClick={() => toggleOption(opt)}
              >
                <div className="ask-option-num">{oi + 1}</div>
                <div className="ask-option-body">
                  <div className="ask-option-label">{label}</div>
                  {desc && <div className="ask-option-desc">{desc}</div>}
                </div>
                <div className="ask-option-check">{isSel ? '✓' : ''}</div>
              </div>
            );
          })}
        </div>

        <div className="ask-modal-free-text">
          <input
            type="text"
            placeholder={q.allow_free_text !== false ? '告诉 Fairy 你的想法' : ''}
            value={freeText}
            onChange={e => { setFreeText(e.target.value); renewDeadline(); }}
            onKeyDown={e => { if (e.key === 'Enter') handleNext(); }}
          />
        </div>

        <div className="ask-modal-footer">
          <span className="ask-modal-skip" onClick={skipOnce}>跳过全部</span>
          <span className="ask-modal-timer">
            {secondsLeft == null
              ? '等待回复'
              : secondsLeft > 0
                ? secondsLeft + 's 后自动继续'
                : '正在自动继续…'}
          </span>
          <button className="ask-modal-next" onClick={handleNext}>
            {isLast ? '完成' : '下一步'}
          </button>
        </div>
      </div>
    </div>
  );
}
