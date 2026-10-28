import test from 'node:test';
import assert from 'node:assert/strict';

import {
  buildMicConstraints,
  normalizeAudioInputs,
  pickMicRecorderMime,
  pickSpokenReplyText,
  readMicDeviceId,
  serverEventState,
  shouldAutoStartMic,
  shouldReconnectVoiceSocket,
  shouldSubmitVoiceFinal,
  voicePhaseIsAwake,
  writeMicDeviceId,
} from './voiceInput.js';


test('ready status unlocks a still-loading microphone session', () => {
  const state = serverEventState(
    { type: 'status', status: 'ready' },
    'loading',
  );
  assert.deepEqual(state, {
    phase: 'waiting',
    ready: true,
    wakeAck: '',
  });
});


test('waiting wake state also unlocks the microphone session', () => {
  const state = serverEventState(
    { type: 'wake_state', state: 'waiting' },
    'loading',
  );
  assert.deepEqual(state, {
    phase: 'waiting',
    ready: true,
    wakeAck: '',
  });
});


test('wake event produces the visible and spoken acknowledgement exactly once', () => {
  const state = serverEventState({ type: 'wake' }, 'waiting');
  assert.deepEqual(state, {
    phase: 'awake',
    ready: true,
    wakeAck: '我在',
    wakeSpeech: '我在。',
    wakeAudioUrl: '/wake-ack-wozai.wav',
  });
  assert.equal(
    serverEventState({ type: 'partial', text: '打开设置' }, 'awake').wakeAck,
    '',
  );
});


test('selected microphone is passed to getUserMedia by exact device id', () => {
  assert.deepEqual(buildMicConstraints('mic-realtek'), {
    audio: {
      echoCancellation: true,
      noiseSuppression: true,
      autoGainControl: true,
      channelCount: { ideal: 1 },
      deviceId: { exact: 'mic-realtek' },
    },
  });
  assert.deepEqual(buildMicConstraints(''), {
    audio: {
      echoCancellation: true,
      noiseSuppression: true,
      autoGainControl: true,
      channelCount: { ideal: 1 },
    },
  });
});


test('audio input list is deduplicated and keeps usable labels', () => {
  assert.deepEqual(normalizeAudioInputs([
    { kind: 'videoinput', deviceId: 'cam', label: 'Camera' },
    { kind: 'audioinput', deviceId: 'mic-1', label: 'Realtek 麦克风阵列' },
    { kind: 'audioinput', deviceId: 'mic-1', label: 'duplicate' },
    { kind: 'audioinput', deviceId: 'mic-2', label: '' },
  ]), [
    { deviceId: 'mic-1', label: 'Realtek 麦克风阵列' },
    { deviceId: 'mic-2', label: '麦克风 2' },
  ]);
});


test('microphone prefers a compressed Opus MediaRecorder format', () => {
  const Recorder = {
    isTypeSupported: type => type === 'audio/webm;codecs=opus',
  };
  assert.equal(pickMicRecorderMime(Recorder), 'audio/webm;codecs=opus');
  assert.equal(pickMicRecorderMime(null), '');
  assert.equal(
    pickMicRecorderMime({ isTypeSupported: () => false }),
    '',
  );
});


test('selected microphone persists across visits', () => {
  const values = new Map();
  const storage = {
    getItem: key => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, value),
    removeItem: key => values.delete(key),
  };
  writeMicDeviceId('mic-realtek', storage);
  assert.equal(readMicDeviceId(storage), 'mic-realtek');
  writeMicDeviceId('', storage);
  assert.equal(readMicDeviceId(storage), '');
});


test('closed voice sockets reconnect unless capture is being deliberately stopped', () => {
  assert.equal(shouldReconnectVoiceSocket({ stopping: false, finished: false }), true);
  assert.equal(shouldReconnectVoiceSocket({ stopping: true, finished: false }), false);
  assert.equal(shouldReconnectVoiceSocket({ stopping: false, finished: true }), false);
});


test('TTS speaks only the reply text that was actually shown in the dialogue', () => {
  assert.equal(
    pickSpokenReplyText(['中间回复', '最终可见回复'], '后台未显示的 final'),
    '最终可见回复',
  );
  assert.equal(pickSpokenReplyText([], '非流式可见回复'), '非流式可见回复');
  assert.equal(pickSpokenReplyText(['已显示回复'], ''), '已显示回复');
  assert.equal(pickSpokenReplyText([], ''), '');
});


test('microphone stays resident unless blocked or the agent is processing', () => {
  assert.equal(shouldAutoStartMic({ recording: false, blocked: false, status: 'idle' }), true);
  assert.equal(shouldAutoStartMic({ recording: true, blocked: false, status: 'idle' }), false);
  assert.equal(shouldAutoStartMic({ recording: false, blocked: true, status: 'idle' }), false);
  assert.equal(shouldAutoStartMic({ recording: false, blocked: false, status: 'thinking' }), false);
});


test('unawakened background transcripts are never submitted as user messages', () => {
  assert.equal(
    shouldSubmitVoiceFinal({ awake: false, stopping: false, wakeMode: true }),
    false,
  );
  assert.equal(
    shouldSubmitVoiceFinal({ awake: true, stopping: true, wakeMode: true }),
    false,
  );
  assert.equal(
    shouldSubmitVoiceFinal({ awake: true, stopping: false, wakeMode: true }),
    true,
  );
});


test('conversation phase keeps follow-up voice turns submittable', () => {
  assert.equal(voicePhaseIsAwake('conversation'), true);
  assert.equal(voicePhaseIsAwake('awake'), true);
  assert.equal(voicePhaseIsAwake('waiting'), false);
});
