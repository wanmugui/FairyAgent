import test from 'node:test';
import assert from 'node:assert/strict';

import {
  isAskUserResolutionEvent,
  submitAskUserRenew,
  submitAskUserReply,
} from './askUser.js';


test('ask answers are delivered directly to the blocked tool endpoint', async () => {
  const calls = [];
  const fetchImpl = async (url, options) => {
    calls.push({ url, options });
    return {
      ok: true,
      json: async () => ({ ok: true, index: 3, answered: true }),
    };
  };

  const result = await submitAskUserReply(
    { q1: ['只数到5'] },
    fetchImpl,
  );

  assert.equal(calls.length, 1);
  assert.equal(calls[0].url, '/api/ask-user/reply');
  assert.equal(calls[0].options.method, 'POST');
  assert.deepEqual(
    JSON.parse(calls[0].options.body),
    { answer: { q1: ['只数到5'] } },
  );
  assert.deepEqual(result, { ok: true, index: 3, answered: true });
});


test('ask reply failures are visible to the voice surface', async () => {
  const fetchImpl = async () => ({
    ok: false,
    status: 409,
    json: async () => ({ ok: false, error: 'no unanswered ask' }),
  });

  await assert.rejects(
    submitAskUserReply({ confirmed: true }, fetchImpl),
    /no unanswered ask/,
  );
});


test('ask replies are scoped to the current call id', async () => {
  const calls = [];
  const fetchImpl = async (url, options) => {
    calls.push({ url, options });
    return { ok: true, json: async () => ({ ok: true }) };
  };

  await submitAskUserReply({ confirmed: true }, fetchImpl, 'call_ask_1');

  assert.deepEqual(
    JSON.parse(calls[0].options.body),
    { answer: { confirmed: true }, call_id: 'call_ask_1' },
  );
});


test('ask interaction renews the backend-owned deadline', async () => {
  const calls = [];
  const fetchImpl = async (url, options) => {
    calls.push({ url, options });
    return {
      ok: true,
      json: async () => ({ ok: true, deadline_at_ms: 123456, remaining_seconds: 30 }),
    };
  };

  const result = await submitAskUserRenew('call_ask_2', fetchImpl);

  assert.equal(calls.length, 1);
  assert.equal(calls[0].url, '/api/ask-user/renew');
  assert.equal(calls[0].options.method, 'POST');
  assert.deepEqual(JSON.parse(calls[0].options.body), { call_id: 'call_ask_2' });
  assert.equal(result.remaining_seconds, 30);
});


test('ask modal closes only for its own resolved ask_user call', () => {
  const pending = { callId: 'call_ask_3' };

  assert.equal(isAskUserResolutionEvent({ tool: 'ask_user', call_id: 'call_ask_3' }, pending), true);
  assert.equal(isAskUserResolutionEvent({ tool: 'ask_user', call_id: 'other' }, pending), false);
  assert.equal(isAskUserResolutionEvent({ tool: 'read_file', call_id: 'call_ask_3' }, pending), false);
});
