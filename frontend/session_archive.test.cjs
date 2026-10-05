const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
const zlib = require('node:zlib');

const { createSessionArchive } = require('./session_archive.cjs');

function readLocalZipEntries(archive) {
  const entries = [];
  let offset = 0;
  while (offset + 4 <= archive.length && archive.readUInt32LE(offset) === 0x04034b50) {
    const method = archive.readUInt16LE(offset + 8);
    const compressedSize = archive.readUInt32LE(offset + 18);
    const nameLength = archive.readUInt16LE(offset + 26);
    const extraLength = archive.readUInt16LE(offset + 28);
    const nameStart = offset + 30;
    const dataStart = nameStart + nameLength + extraLength;
    const compressed = archive.subarray(dataStart, dataStart + compressedSize);
    const content = method === 8 ? zlib.inflateRawSync(compressed) : compressed;
    entries.push({ name: archive.subarray(nameStart, nameStart + nameLength).toString('utf8'), content });
    offset = dataStart + compressedSize;
  }
  return entries;
}

test('session archive creates a portable ZIP with the session directory as root', () => {
  const sessions = fs.mkdtempSync(path.join(os.tmpdir(), 'session-archive-'));
  const sessionDir = path.join(sessions, 'chat-1');
  fs.mkdirSync(path.join(sessionDir, 'subtasks'), { recursive: true });
  fs.writeFileSync(path.join(sessionDir, 'chat-1.json'), '{"messages":[]}');
  fs.writeFileSync(path.join(sessionDir, 'subtasks', '研究.json'), 'result');

  const archive = createSessionArchive(sessionDir);
  const entries = readLocalZipEntries(archive);

  assert.deepEqual(entries.map(entry => entry.name), ['chat-1/chat-1.json', 'chat-1/subtasks/研究.json']);
  assert.equal(entries[0].content.toString(), '{"messages":[]}');
  assert.equal(entries[1].content.toString(), 'result');
});
