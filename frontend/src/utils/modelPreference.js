export const MODEL_STORAGE_KEY = 'fairy.selectedModel';
export const FALLBACK_ENABLED_STORAGE_KEY = 'fairy.autoFallback';
export const FALLBACK_MODEL_STORAGE_KEY = 'fairy.fallbackModel';

export function isUserModelId(id) {
  return typeof id === 'string' && id.startsWith('user_');
}

export function listUserModelIds(models) {
  const list = Array.isArray(models) ? models : [];
  return list.filter((m) => m && (m.user || isUserModelId(m.id))).map((m) => m.id);
}

export function readStoredModel() {
  try {
    return localStorage.getItem(MODEL_STORAGE_KEY) || '';
  } catch {
    return '';
  }
}

export function writeStoredModel(modelId) {
  try {
    if (modelId) localStorage.setItem(MODEL_STORAGE_KEY, modelId);
  } catch {}
}

export function readAutoFallback() {
  try {
    const stored = localStorage.getItem(FALLBACK_ENABLED_STORAGE_KEY);
    return stored === null ? true : stored === '1';
  } catch {
    return true;
  }
}

export function writeAutoFallback(enabled) {
  try {
    localStorage.setItem(FALLBACK_ENABLED_STORAGE_KEY, enabled ? '1' : '0');
  } catch {}
}

export function readFallbackModel() {
  try {
    return localStorage.getItem(FALLBACK_MODEL_STORAGE_KEY) || '';
  } catch {
    return '';
  }
}

export function writeFallbackModel(modelId) {
  try {
    if (modelId) localStorage.setItem(FALLBACK_MODEL_STORAGE_KEY, modelId);
    else localStorage.removeItem(FALLBACK_MODEL_STORAGE_KEY);
  } catch {}
}

export function pickValidModel(models, preferred) {
  const list = Array.isArray(models) ? models : [];
  if (!list.length) return preferred || '';
  const wanted = preferred || readStoredModel();
  const stored = list.find((item) => item.id === wanted);
  if (stored && stored.available !== false) return wanted;
  return (list.find((item) => item.available !== false) || list[0]).id;
}

export function pickValidFallbackModel(models, preferred, selectedModel) {
  const list = Array.isArray(models) ? models : [];
  if (!preferred) return '';
  if (!list.length) return preferred;
  const candidates = list.filter(item => item && item.id !== selectedModel && item.available !== false);
  const stored = candidates.find(item => item.id === preferred);
  return stored ? stored.id : '';
}
