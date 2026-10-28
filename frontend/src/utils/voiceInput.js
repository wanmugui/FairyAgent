export const MIC_DEVICE_STORAGE_KEY = 'fairy.voice.micDeviceId';
export const WAKE_ACK_TEXT = '我在';
export const WAKE_SPEECH_TEXT = '我在。';
export const WAKE_ACK_AUDIO_URL = '/wake-ack-wozai.wav';


export function readMicDeviceId(storage = globalThis.localStorage) {
  try {
    return String(storage?.getItem(MIC_DEVICE_STORAGE_KEY) || '').trim();
  } catch {
    return '';
  }
}


export function writeMicDeviceId(deviceId, storage = globalThis.localStorage) {
  const value = String(deviceId || '').trim();
  try {
    if (value) storage?.setItem(MIC_DEVICE_STORAGE_KEY, value);
    else storage?.removeItem(MIC_DEVICE_STORAGE_KEY);
  } catch {
    // Storage can be unavailable in private browsing or embedded webviews.
  }
  return value;
}


export function buildMicConstraints(deviceId) {
  const selected = String(deviceId || '').trim();
  return {
    audio: {
      echoCancellation: true,
      noiseSuppression: true,
      autoGainControl: true,
      channelCount: { ideal: 1 },
      ...(selected ? { deviceId: { exact: selected } } : {}),
    },
  };
}


export const MIC_RECORDER_MIME_TYPES = [
  'audio/webm;codecs=opus',
  'audio/ogg;codecs=opus',
  'audio/webm',
];


export function pickMicRecorderMime(Recorder = globalThis.MediaRecorder) {
  if (!Recorder || typeof Recorder.isTypeSupported !== 'function') return '';
  return MIC_RECORDER_MIME_TYPES.find(type => Recorder.isTypeSupported(type)) || '';
}


export function normalizeAudioInputs(devices) {
  const seen = new Set();
  const inputs = [];
  for (const device of (Array.isArray(devices) ? devices : [])) {
    if (!device || device.kind !== 'audioinput') continue;
    const deviceId = String(device.deviceId || '').trim();
    if (!deviceId || seen.has(deviceId)) continue;
    seen.add(deviceId);
    inputs.push({
      deviceId,
      label: String(device.label || '').trim() || `麦克风 ${inputs.length + 1}`,
    });
  }
  return inputs;
}


export function serverEventState(event, currentPhase = 'loading') {
  if (!event || typeof event !== 'object') {
    return { phase: currentPhase, ready: false, wakeAck: '' };
  }
  if (event.type === 'status') {
    if (event.status === 'ready') {
      return { phase: 'waiting', ready: true, wakeAck: '' };
    }
    if (event.status === 'loading') {
      return { phase: 'loading', ready: false, wakeAck: '' };
    }
  }
  if (event.type === 'wake_state') {
    return {
      phase: event.state === 'conversation' ? 'conversation' : 'waiting',
      ready: true,
      wakeAck: '',
    };
  }
  if (event.type === 'wake') {
    return {
      phase: 'awake',
      ready: true,
      wakeAck: WAKE_ACK_TEXT,
      wakeSpeech: WAKE_SPEECH_TEXT,
      wakeAudioUrl: WAKE_ACK_AUDIO_URL,
    };
  }
  if (event.type === 'reset') {
    return { phase: 'waiting', ready: true, wakeAck: '' };
  }
  if (event.type === 'listen_partial') {
    return { phase: 'hearing', ready: true, wakeAck: '' };
  }
  if (event.type === 'partial') {
    return { phase: 'awake', ready: true, wakeAck: '' };
  }
  return { phase: currentPhase, ready: currentPhase !== 'loading', wakeAck: '' };
}

export function shouldReconnectVoiceSocket(state) {
  return !(state && (state.stopping || state.finished));
}


export function pickSpokenReplyText(visibleReplies, fallback = '') {
  const shown = [...(visibleReplies || [])]
    .reverse()
    .find(value => String(value || '').trim());
  return String(shown || fallback || '').trim();
}


export function shouldAutoStartMic({ recording, blocked, status }) {
  return !recording && !blocked && status !== 'thinking';
}


export function shouldSubmitVoiceFinal({ awake, stopping, wakeMode }) {
  return Boolean(wakeMode && awake && !stopping);
}


export function voicePhaseIsAwake(phase) {
  return phase === 'awake' || phase === 'conversation';
}
