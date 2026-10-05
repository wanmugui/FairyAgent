// Fairy 仅保留基础与 ZZZ 两套皮肤。
export const THEMES = {
  base: { label: '基础', acc: '#4a6cf7', acc2: '#2563eb', soft: '#eef2ff',
    grad: 'linear-gradient(90deg,#4a6cf7,#2563eb)',
    bgA: 'transparent', bgB: 'transparent', bgC: 'transparent' },
  zzz: { label: '绝区零 · ZZZ', acc: '#c4f500', acc2: '#ff6b22', soft: 'rgba(196,245,0,.12)',
    grad: 'linear-gradient(135deg,#c4f500,#ff9d36)',
    bgA: 'rgba(196,245,0,.08)', bgB: 'rgba(85,196,231,.06)', bgC: 'rgba(255,107,34,.05)' },
};
const KEY = 'pptui_theme';
const VERSION_KEY = 'pptui_theme_version';
const VERSION = '3';
const DEFAULT_THEME = 'zzz';

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
    metaTheme.setAttribute(
      'content',
      resolvedKey === 'base' ? '#f3f4f6' : '#080909'
    );
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
