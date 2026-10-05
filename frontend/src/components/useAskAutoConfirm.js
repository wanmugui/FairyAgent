import { useCallback, useEffect, useRef, useState } from 'react';

export const ASK_USER_AUTO_CONFIRM_SECONDS = 30;

// Runs the default confirmation after a short idle period. The ref prevents a
// timer and a near-simultaneous click from submitting the same ask_user reply
// twice. Changing resetKey starts a fresh countdown for the next question.
export function useAskAutoConfirm(onConfirm, resetKey) {
  const [secondsLeft, setSecondsLeft] = useState(ASK_USER_AUTO_CONFIRM_SECONDS);
  const completedRef = useRef(false);

  const resetCountdown = useCallback(() => {
    if (!completedRef.current) setSecondsLeft(ASK_USER_AUTO_CONFIRM_SECONDS);
  }, []);

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
    setSecondsLeft(ASK_USER_AUTO_CONFIRM_SECONDS);
  }, [resetKey]);

  useEffect(() => {
    if (completedRef.current) return undefined;
    if (secondsLeft <= 0) {
      confirm();
      return undefined;
    }
    const timer = window.setTimeout(() => setSecondsLeft(value => value - 1), 1000);
    return () => window.clearTimeout(timer);
  }, [secondsLeft, confirm]);

  return { secondsLeft, resetCountdown, confirm, cancel };
}
