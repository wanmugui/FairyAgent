export const VOICE_AUTO_READ_KEY = 'fairy.voiceAutoRead';

export function readVoiceAutoRead() {
  try {
    const raw = localStorage.getItem(VOICE_AUTO_READ_KEY);
    if (raw === null) return true; // 默认开启
    return raw === '1' || raw === 'true';
  } catch {
    return true;
  }
}

export function writeVoiceAutoRead(enabled) {
  try {
    localStorage.setItem(VOICE_AUTO_READ_KEY, enabled ? '1' : '0');
  } catch {}
}