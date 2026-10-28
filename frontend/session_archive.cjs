const fs = require('node:fs');
const path = require('node:path');
const zlib = require('node:zlib');

const crcTable = (() => {
  const table = new Uint32Array(256);
  for (let index = 0; index < table.length; index++) {
    let value = index;
    for (let bit = 0; bit < 8; bit++) value = (value >>> 1) ^ ((value & 1) ? 0xedb88320 : 0);
    table[index] = value >>> 0;
  }
  return table;
})();

function crc32(data) {
  let value = 0xffffffff;
  for (const byte of data) value = (value >>> 8) ^ crcTable[(value ^ byte) & 0xff];
  return (value ^ 0xffffffff) >>> 0;
}

function dosDateTime(date) {
  const year = Math.max(1980, date.getFullYear());
  return {
    time: (date.getSeconds() >> 1) | (date.getMinutes() << 5) | (date.getHours() << 11),
    date: date.getDate() | ((date.getMonth() + 1) << 5) | ((year - 1980) << 9),
  };
}

function collectArchiveFiles(root, prefix) {
  const files = [];
  const walk = (directory, relative) => {
    const children = fs.readdirSync(directory, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name));
    for (const child of children) {
      const fullPath = path.join(directory, child.name);
      const archivePath = relative ? `${relative}/${child.name}` : child.name;
      if (child.isDirectory()) {
        walk(fullPath, archivePath);
      } else if (child.isFile()) {
        files.push({ fullPath, archivePath: `${prefix}/${archivePath}` });
      }
    }
  };
  walk(root, '');
  return files;
}

function createSessionArchive(sessionDirectory) {
  const root = path.resolve(sessionDirectory);
  if (!fs.statSync(root).isDirectory()) throw new Error(`session directory is not a directory: ${root}`);
  const files = collectArchiveFiles(root, path.basename(root));
  const locals = [];
  const central = [];
  let offset = 0;

  for (const file of files) {
    const data = fs.readFileSync(file.fullPath);
    const compressed = zlib.deflateRawSync(data);
    const name = Buffer.from(file.archivePath.replaceAll(path.sep, '/'), 'utf8');
    const { time, date } = dosDateTime(fs.statSync(file.fullPath).mtime);
    const checksum = crc32(data);
    if (data.length > 0xffffffff || compressed.length > 0xffffffff || offset > 0xffffffff) {
      throw new Error('session archive exceeds ZIP32 size limits');
    }

    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt16LE(0x0800, 6);
    local.writeUInt16LE(8, 8);
    local.writeUInt16LE(time, 10);
    local.writeUInt16LE(date, 12);
    local.writeUInt32LE(checksum, 14);
    local.writeUInt32LE(compressed.length, 18);
    local.writeUInt32LE(data.length, 22);
    local.writeUInt16LE(name.length, 26);
    local.writeUInt16LE(0, 28);
    locals.push(local, name, compressed);

    const directory = Buffer.alloc(46);
    directory.writeUInt32LE(0x02014b50, 0);
    directory.writeUInt16LE(0x0314, 4);
    directory.writeUInt16LE(20, 6);
    directory.writeUInt16LE(0x0800, 8);
    directory.writeUInt16LE(8, 10);
    directory.writeUInt16LE(time, 12);
    directory.writeUInt16LE(date, 14);
    directory.writeUInt32LE(checksum, 16);
    directory.writeUInt32LE(compressed.length, 20);
    directory.writeUInt32LE(data.length, 24);
    directory.writeUInt16LE(name.length, 28);
    directory.writeUInt16LE(0, 30);
    directory.writeUInt16LE(0, 32);
    directory.writeUInt16LE(0, 34);
    directory.writeUInt16LE(0, 36);
    directory.writeUInt32LE(0, 38);
    directory.writeUInt32LE(offset, 42);
    central.push(directory, name);
    offset += local.length + name.length + compressed.length;
  }

  const centralSize = central.reduce((size, item) => size + item.length, 0);
  if (files.length > 0xffff || centralSize > 0xffffffff || offset > 0xffffffff) {
    throw new Error('session archive exceeds ZIP32 entry limits');
  }
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(0, 4);
  end.writeUInt16LE(0, 6);
  end.writeUInt16LE(files.length, 8);
  end.writeUInt16LE(files.length, 10);
  end.writeUInt32LE(centralSize, 12);
  end.writeUInt32LE(offset, 16);
  end.writeUInt16LE(0, 20);
  return Buffer.concat([...locals, ...central, end]);
}

module.exports = { createSessionArchive };
