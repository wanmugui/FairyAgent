#!/usr/bin/env node
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';
import { fileURLToPath } from 'node:url';

export const PROTECTED_KEY_SEED = 'fairy-public-protected-assets-v1|c4f38b2e7a5d4091';
const MAGIC = Buffer.from('FAIRYCB1', 'ascii');

export function assertProtectedPath(relPath) {
  const normalized = path.posix.normalize(String(relPath).replaceAll('\\', '/'));
  if (
    !normalized ||
    normalized === '.' ||
    normalized.startsWith('/') ||
    normalized === '..' ||
    normalized.startsWith('../')
  ) {
    throw new Error(`invalid protected path: ${relPath}`);
  }
  return normalized;
}

function walkFiles(root) {
  if (!fs.existsSync(root)) return [];
  const out = [];
  for (const entry of fs.readdirSync(root, { withFileTypes: true })) {
    const full = path.join(root, entry.name);
    if (entry.isDirectory()) out.push(...walkFiles(full));
    else if (entry.isFile()) out.push(full);
  }
  return out.sort();
}

function canonicalText(value) {
  return String(value)
    .replace(/^\uFEFF/, '')
    .replace(/\r\n?/g, '\n');
}

export function buildPayload(sourceRoot, deps = {}) {
  const readFile = deps.readFile || (file => fs.readFileSync(file, 'utf8'));
  const walk = deps.walk || walkFiles;
  const exists = deps.exists || fs.existsSync;
  const roots = [
    path.join(sourceRoot, 'config', 'system'),
    path.join(sourceRoot, 'config', 'modules'),
  ];
  const candidates = roots.flatMap(root => walk(root).filter(file => file.endsWith('.md')));
  const schemas = path.join(sourceRoot, 'config', 'tools', 'schemas.json');
  if (exists(schemas)) candidates.push(schemas);

  const files = {};
  for (const file of candidates) {
    const rel = assertProtectedPath(path.relative(sourceRoot, file));
    files[rel] = canonicalText(readFile(file));
  }
  if (!files['config/system/zh.md']) {
    throw new Error('protected bundle requires config/system/zh.md');
  }
  if (!files['config/tools/schemas.json']) {
    throw new Error('protected bundle requires config/tools/schemas.json');
  }
  return {
    version: 1,
    files: Object.fromEntries(Object.entries(files).sort(([a], [b]) => a.localeCompare(b))),
  };
}

export function encryptBundle(payload) {
  const key = crypto.createHash('sha256').update(PROTECTED_KEY_SEED).digest();
  const nonce = crypto.randomBytes(12);
  const cipher = crypto.createCipheriv('aes-256-gcm', key, nonce);
  const plaintext = Buffer.from(JSON.stringify(payload), 'utf8');
  const ciphertext = Buffer.concat([cipher.update(plaintext), cipher.final()]);
  return Buffer.concat([MAGIC, nonce, ciphertext, cipher.getAuthTag()]);
}

export function decryptBundleForTest(raw) {
  if (raw.subarray(0, 8).toString('ascii') !== 'FAIRYCB1') {
    throw new Error('invalid protected bundle magic');
  }
  const key = crypto.createHash('sha256').update(PROTECTED_KEY_SEED).digest();
  const nonce = raw.subarray(8, 20);
  const body = raw.subarray(20);
  const decipher = crypto.createDecipheriv('aes-256-gcm', key, nonce);
  decipher.setAuthTag(body.subarray(-16));
  const plaintext = Buffer.concat([
    decipher.update(body.subarray(0, -16)),
    decipher.final(),
  ]);
  return JSON.parse(plaintext.toString('utf8'));
}

function parseArgs(argv) {
  const out = {};
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === '--source') out.source = argv[++i];
    else if (arg === '--output') out.output = argv[++i];
    else throw new Error(`unknown argument: ${arg}`);
  }
  return out;
}

function main() {
  const here = path.dirname(fileURLToPath(import.meta.url));
  const repoRoot = path.resolve(here, '..');
  const args = parseArgs(process.argv.slice(2));
  const sourceRoot = path.resolve(args.source || repoRoot);
  const output = path.resolve(args.output || path.join(repoRoot, 'config', 'protected', 'core.bundle'));
  const payload = buildPayload(sourceRoot);
  const encrypted = encryptBundle(payload);
  fs.mkdirSync(path.dirname(output), { recursive: true });
  fs.writeFileSync(output, encrypted);
  console.log(`[protected-assets] wrote ${Object.keys(payload.files).length} files, ${encrypted.length} bytes -> ${output}`);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    main();
  } catch (error) {
    console.error(`[protected-assets] ${error.message}`);
    process.exitCode = 1;
  }
}
