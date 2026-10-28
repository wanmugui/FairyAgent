function normalizeOptions(options) {
  if (typeof options === 'string') {
    try {
      options = JSON.parse(options);
    } catch {
      options = [];
    }
  }
  return Array.isArray(options) ? options.filter(Boolean) : [];
}

export async function submitAskUserReply(answer, fetchImpl = globalThis.fetch, callId = '') {
  const body = { answer };
  if (callId) body.call_id = callId;
  const response = await fetchImpl('/api/ask-user/reply', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  let payload = null;
  try {
    payload = await response.json();
  } catch {}
  if (!response.ok) {
    throw new Error(payload?.error || `ask reply HTTP ${response.status}`);
  }
  return payload;
}

export async function submitAskUserRenew(callId, fetchImpl = globalThis.fetch) {
  const response = await fetchImpl('/api/ask-user/renew', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ call_id: callId }),
  });
  let payload = null;
  try {
    payload = await response.json();
  } catch {}
  if (!response.ok) {
    throw new Error(payload?.error || `ask renew HTTP ${response.status}`);
  }
  return payload;
}

export function isAskUserResolutionEvent(event, pending) {
  const tool = event?.tool || event?.name || '';
  if (tool !== 'ask_user') return false;
  const eventCallId = String(event?.call_id || event?.tool_call_id || '').trim();
  const pendingCallId = String(pending?.callId || '').trim();
  return !eventCallId || !pendingCallId || eventCallId === pendingCallId;
}

export function normalizeAskQuestions(event) {
  let questions = event?.questions;
  if (typeof questions === 'string') {
    try {
      questions = JSON.parse(questions);
    } catch {
      questions = [];
    }
  }
  if (!Array.isArray(questions)) questions = [];

  if (!questions.length && Array.isArray(event?.options)) {
    questions = [{
      id: 'q1',
      question: event.question || event.title || '',
      options: event.options,
      allow_free_text: event.allow_free_text,
      multi_select: event.multi_select,
    }];
  }

  return questions
    .map((question, index) => {
      if (typeof question === 'string') {
        return { id: `q${index + 1}`, question, options: [] };
      }
      if (!question || typeof question !== 'object') return null;
      return {
        ...question,
        id: question.id || `q${index + 1}`,
        options: normalizeOptions(question.options),
      };
    })
    .filter(Boolean);
}
