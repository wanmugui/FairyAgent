import { test } from 'node:test';
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import path from 'node:path';
import {
  PROTECTED_KEY_SEED,
  assertProtectedPath,
  buildPayload,
  encryptBundle,
  decryptBundleForTest,
} from './build_protected_assets.mjs';

test('buildPayload includes only supported prompt and schema roots', () => {
  const repo = path.resolve('/repo');
  const payload = buildPayload(repo, {
    readFile: file => {
      if (file.endsWith('schemas.json')) return '{"read_file":{}}';
      if (file.split(path.sep).includes('system')) return 'system prompt';
      return 'module prompt';
    },
    walk: root => {
      if (root.split(path.sep).includes('system')) return [path.join(root, 'zh.md')];
      if (root.split(path.sep).includes('modules')) {
        return [
          path.join(root, 'summary', 'zh.md'),
          path.join(root, 'channels', 'voice', 'zh.md'),
        ];
      }
      return [];
    },
    exists: file => file.endsWith('schemas.json'),
  });
  assert.deepEqual(Object.keys(payload.files).sort(), [
    'config/modules/channels/voice/zh.md',
    'config/modules/summary/zh.md',
    'config/system/zh.md',
    'config/tools/schemas.json',
  ]);
});

test('buildPayload canonicalizes BOM and CRLF in protected text', () => {
  const repo = path.resolve('/repo');
  const payload = buildPayload(repo, {
    readFile: file => {
      if (file.endsWith('schemas.json')) return '{\r\n"read_file":{}\r\n}';
      return '\uFEFFprompt\r\nline';
    },
    walk: root => {
      if (root.split(path.sep).includes('system')) return [path.join(root, 'zh.md')];
      return [];
    },
    exists: file => file.endsWith('schemas.json'),
  });
  assert.equal(payload.files['config/system/zh.md'], 'prompt\nline');
  assert.equal(payload.files['config/tools/schemas.json'], '{\n"read_file":{}\n}');
});

test('assertProtectedPath rejects escapes and absolute paths', () => {
  assert.throws(() => assertProtectedPath('../secret.txt'), /invalid protected path/);
  assert.throws(() => assertProtectedPath('/etc/passwd'), /invalid protected path/);
  assert.equal(assertProtectedPath('config/system/zh.md'), 'config/system/zh.md');
});

test('encryptBundle uses FAIRYCB1 magic and round-trips', () => {
  const raw = encryptBundle({ version: 1, files: { 'config/system/zh.md': 'prompt' } });
  assert.equal(raw.subarray(0, 8).toString('ascii'), 'FAIRYCB1');

  const key = crypto.createHash('sha256').update(PROTECTED_KEY_SEED).digest();
  const nonce = raw.subarray(8, 20);
  const ciphertext = raw.subarray(20);
  const decipher = crypto.createDecipheriv('aes-256-gcm', key, nonce);
  decipher.setAuthTag(ciphertext.subarray(-16));
  const plaintext = Buffer.concat([
    decipher.update(ciphertext.subarray(0, -16)),
    decipher.final(),
  ]);
  assert.deepEqual(decryptBundleForTest(raw), JSON.parse(plaintext.toString('utf8')));
});
