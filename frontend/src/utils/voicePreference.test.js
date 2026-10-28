import test from 'node:test';
import assert from 'node:assert/strict';

import {
  MAIN_VOICE_AUTO_READ_KEY,
  VOICE_PAGE_AUTO_READ_KEY,
  readVoiceAutoRead,
  writeVoiceAutoRead,
} from './voicePreference.js';


function memoryStorage(initial = {}) {
  const values = new Map(Object.entries(initial));
  return {
    getItem: key => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, value),
    removeItem: key => values.delete(key),
  };
}


test('main and immersive voice pages store independent playback switches', () => {
  const storage = memoryStorage();
  writeVoiceAutoRead(false, 'main', storage);
  assert.equal(readVoiceAutoRead('main', storage), false);
  assert.equal(readVoiceAutoRead('voice', storage), true);

  writeVoiceAutoRead(true, 'voice', storage);
  assert.equal(readVoiceAutoRead('main', storage), false);
  assert.equal(readVoiceAutoRead('voice', storage), true);
  assert.equal(storage.getItem(MAIN_VOICE_AUTO_READ_KEY), '0');
  assert.equal(storage.getItem(VOICE_PAGE_AUTO_READ_KEY), '1');
});


test('legacy shared preference seeds only the main console', () => {
  const storage = memoryStorage({ 'fairy.voiceAutoRead': '0' });
  assert.equal(readVoiceAutoRead('main', storage), false);
  assert.equal(readVoiceAutoRead('voice', storage), true);
});
