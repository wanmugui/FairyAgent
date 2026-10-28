export const MAIN_VOICE_AUTO_READ_KEY = 'fairy.mainVoiceAutoRead';
export const VOICE_PAGE_AUTO_READ_KEY = 'fairy.voicePageAutoRead';
export const LEGACY_VOICE_AUTO_READ_KEY = 'fairy.voiceAutoRead';

function storageKey(scope) {
  return scope === 'voice' ? VOICE_PAGE_AUTO_READ_KEY : MAIN_VOICE_AUTO_READ_KEY;
}

export function readVoiceAutoRead(scope = 'main', storage = globalThis.localStorage) {
  try {
    let raw = storage?.getItem(storageKey(scope)) ?? null;
    if (raw === null && scope !== 'voice') {
      raw = storage?.getItem(LEGACY_VOICE_AUTO_READ_KEY) ?? null;
    }
    if (raw === null) return true; // 默认开启
    return raw === '1' || raw === 'true';
  } catch {
    return true;
  }
}

export function writeVoiceAutoRead(enabled, scope = 'main', storage = globalThis.localStorage) {
  try {
    storage?.setItem(storageKey(scope), enabled ? '1' : '0');
  } catch {}
}
