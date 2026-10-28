import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const STYLES = path.join(HERE, '..', 'src', 'styles');

const themesCss = fs.readFileSync(path.join(STYLES, 'themes.css'), 'utf8');
const baseCss = fs.readFileSync(path.join(STYLES, 'base-skin.css'), 'utf8');
const zzzCss = fs.readFileSync(path.join(STYLES, 'zzz-skin.css'), 'utf8');

test('public themes.css does not reference the private P5 theme', () => {
  assert.doesNotMatch(themesCss, /data-theme=['"]p5['"]/i);
});

test('public distribution includes the ZZZ skin', () => {
  assert.match(zzzCss, /html\[data-theme=['"]zzz['"]\]/i);
});

test('base skin keeps its neutral accent', () => {
  const baseAccent = baseCss.match(/--ui-accent:\s*(#[0-9a-fA-F]{3,8})/);
  assert.ok(baseAccent, 'base-skin.css must declare --ui-accent');
  assert.equal(baseAccent[1].toLowerCase(), '#4a6cf7');
});

test('base skin hides decorative layers inherited from the light theme', () => {
  assert.match(
    baseCss,
    /html\[data-theme='base'\] body::before,\s*html\[data-theme='base'\] \.main::before,\s*html\[data-theme='base'\] header::after\s*\{[^}]*display:\s*none/,
  );
});

test('base skin paints the shell with its neutral background', () => {
  assert.match(
    baseCss,
    /html\[data-theme='base'\],\s*html\[data-theme='base'\] body\s*\{\s*background:\s*#f3f4f6;/,
  );
});
