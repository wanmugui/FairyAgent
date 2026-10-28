import assert from 'node:assert/strict';
import test from 'node:test';

import { modelDownloadUrls } from './fetch-models.mjs';
import { MODELS } from './model-manifest.mjs';

test('mutable upstream documentation is not part of model readiness', () => {
  const whisper = MODELS.find(model => model.id === 'faster-whisper-base');
  assert.ok(whisper);
  assert.equal(whisper.files.some(file => file.name === 'README.md'), false);
});

test('HuggingFace downloads prefer the mirror by default', () => {
  assert.deepEqual(
    modelDownloadUrls('https://huggingface.co/Systran/faster-whisper-base/resolve/main/model.bin'),
    [
      'https://hf-mirror.com/Systran/faster-whisper-base/resolve/main/model.bin',
      'https://huggingface.co/Systran/faster-whisper-base/resolve/main/model.bin',
    ],
  );
});

test('official HuggingFace can be forced first', () => {
  assert.deepEqual(
    modelDownloadUrls(
      'https://huggingface.co/OpenMOSS-Team/model/resolve/main/config.json',
      { FAIRY_MODEL_OFFICIAL_FIRST: '1' },
    ),
    [
      'https://huggingface.co/OpenMOSS-Team/model/resolve/main/config.json',
      'https://hf-mirror.com/OpenMOSS-Team/model/resolve/main/config.json',
    ],
  );
});

test('non-HuggingFace URLs keep their original order', () => {
  assert.deepEqual(
    modelDownloadUrls('https://github.com/k2-fsa/sherpa-onnx/releases/download/a.tar.bz2'),
    ['https://github.com/k2-fsa/sherpa-onnx/releases/download/a.tar.bz2'],
  );
});
