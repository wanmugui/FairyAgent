import { useCallback, useEffect, useRef, useState } from 'react';

// Fallback only. The backend deadline (builtin.AskUserAutoSelectSeconds = 30)
// wins whenever it is published.
export const ASK_USER_AUTO_CONFIRM_SECONDS = 30;

// Runs the default confirmation after an idle period. The ref prevents a timer
// and a near-simultaneous click from submitting the same ask_user reply twice.
// Changing resetKey starts a fresh countdown for the next question.
//
// The default length now comes from the backend (agent publishes
// auto_select_after_seconds on the waiting_user_input event) so there is a
// single clock. The local fallback only applies when the backend publishes no
// deadline -- it must never be shorter, or the two would race.
export function useAskAutoConfirm(onConfirm, resetKey, backendSeconds, onRenew) {
  const deadline = Number.isFinite(backendSeconds) && backendSeconds > 0
    ? Math.ceil(backendSeconds)
    : ASK_USER_AUTO_CONFIRM_SECONDS;
  const [secondsLeft, setSecondsLeft] = useState(deadline);
  const completedRef = useRef(false);
  const lastRenewAtRef = useRef(0);

  const resetCountdown = useCallback(() => {
    if (completedRef.current) return;
    setSecondsLeft(deadline);
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
      .catch(() => {});
  }, [deadline, onRenew]);

  const confirm = useCallback(() => {
    if (completedRef.current) return;
    completedRef.current = true;
    onConfirm();
  }, [onConfirm]);

  const cancel = useCallback(() => {
    completedRef.current = true;
  }, []);

  useEffect(() => {
    completedRef.current = false;
    setSecondsLeft(deadline);
  }, [deadline, resetKey]);

  useEffect(() => {
    if (completedRef.current) return undefined;
    if (secondsLeft <= 0) return undefined;
    const timer = window.setTimeout(() => setSecondsLeft(value => value - 1), 1000);
    return () => window.clearTimeout(timer);
  }, [secondsLeft]);

  return { secondsLeft, resetCountdown, confirm, cancel };
}
