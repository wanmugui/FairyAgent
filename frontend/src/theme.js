// 公开仓分发基础与 ZZZ 皮肤；P5 保持私有。
export const THEMES = {
  base: { label: '基础', acc: '#4a6cf7', acc2: '#2563eb', soft: '#eef2ff',
    grad: 'linear-gradient(90deg,#4a6cf7,#2563eb)',
    bgA: 'transparent', bgB: 'transparent', bgC: 'transparent' },
  zzz: { label: '绝区零 · ZZZ', acc: '#ffd400', acc2: '#00e0e8', soft: 'rgba(255,212,0,.12)',
    grad: 'linear-gradient(135deg,#ffd400,#00e0e8)',
    bgA: 'rgba(255,212,0,.08)', bgB: 'rgba(0,224,232,.06)', bgC: 'rgba(255,45,111,.05)' },
};
const KEY = 'pptui_theme';
const VERSION_KEY = 'pptui_theme_version';
const VERSION = '4';
const DEFAULT_THEME = 'base';

export function applyTheme(key) {
  const resolvedKey = THEMES[key] ? key : 'base';
  const t = THEMES[resolvedKey];
  const r = document.documentElement.style;
  r.setProperty('--acc', t.acc);
  r.setProperty('--acc2', t.acc2);
  r.setProperty('--acc-soft', t.soft);
  r.setProperty('--grad-brand', t.grad);
  r.setProperty('--bgA', t.bgA);
  r.setProperty('--bgB', t.bgB);
  r.setProperty('--bgC', t.bgC);
  document.documentElement.setAttribute('data-theme', resolvedKey);
  const metaTheme = document.querySelector('meta[name="theme-color"]');
  if (metaTheme) {
    const chrome = { base: '#f3f4f6', zzz: '#0b0d0e' };
    metaTheme.setAttribute('content', chrome[resolvedKey] || '#080909');
  }
  try {
    localStorage.setItem(KEY, resolvedKey);
    localStorage.setItem(VERSION_KEY, VERSION);
  } catch { /* ignore */ }
}

export function saveThemePreference(key) {
  const resolvedKey = THEMES[key] ? key : 'base';
  try {
    localStorage.setItem(KEY, resolvedKey);
    localStorage.setItem(VERSION_KEY, VERSION);
  } catch { /* ignore */ }
  return resolvedKey;
}
export function currentTheme() {
  try {
    if (localStorage.getItem(VERSION_KEY) !== VERSION) return DEFAULT_THEME;
    const stored = localStorage.getItem(KEY);
    return THEMES[stored] ? stored : DEFAULT_THEME;
  } catch { return DEFAULT_THEME; }
}

export function initTheme() { applyTheme(currentTheme()); }
