import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  splitSentences,
  cleanTTSText,
  buildTTSChunks,
  isArtifactOnly,
  extractVoiceText,
  selectDialogueSpeechText,
  selectDialogueSpeechSegments,
  truncateAtSentence,
  clampDialogueText,
} from './ttsText.js';

test('splitSentences: splits on 。 and newlines, keeps ！？ inside', () => {
  const { sentences, remainder } = splitSentences('你好，主人！今天有3条消息。\n明天有2条。');
  assert.deepEqual(sentences, ['你好，主人！今天有3条消息。', '明天有2条。']);
  assert.equal(remainder, '');
});

test('cleanTTSText: strips markdown / links / bullets / numbering / space-before-punct', () => {
  const out = cleanTTSText('3. **节假日安排**：\n- 国务院办公厅发布了通知。[详情](https://gov.cn/x) 参考&#x20;链接');
  assert.equal(out, '节假日安排： 国务院办公厅发布了通知。 参考 链接');
});

test('buildTTSChunks: heading fragments (ending in ：) are merged into the next sentence', () => {
  const raw = '1. **《披荆斩棘2026》**：\n\n- 这是一档以“点燃”为核心主题的节目，结合了户外试炼和舞台公演，诠释了新时代的披荆斩棘精神。[详情](https://www.mgtv.com/h/896231.html)\n- \n\n2. **政府工作报告**：\n\n- 2026年3月5日，国务院总理李强在第十四届全国人民代表大会第四次会议上作了政府工作报告，报告涵盖了国家发展的各个方面。[详情](https://www.gov.cn/x)';
  const { sentences } = splitSentences(raw);
  const chunks = buildTTSChunks(sentences);
  assert.equal(chunks.length, 2, JSON.stringify(chunks));
  assert.equal(chunks[0], '《披荆斩棘2026》：这是一档以“点燃”为核心主题的节目，结合了户外试炼和舞台公演，诠释了新时代的披荆斩棘精神。');
  assert.equal(chunks[1], '政府工作报告：2026年3月5日，国务院总理李强在第十四届全国人民代表大会第四次会议上作了政府工作报告，报告涵盖了国家发展的各个方面。');
  // 不允许出现以 ： 结尾的裸标题单独成 chunk（会导致模型跑飞）
  for (const c of chunks) assert.ok(!/[:：]$/.test(c), 'bare heading chunk leaked: ' + c);
});

test('buildTTSChunks: drops artifact-only lines (lone "-" / "1.")', () => {
  assert.equal(isArtifactOnly('-'), true);
  assert.equal(isArtifactOnly('1.'), true);
  const chunks = buildTTSChunks(['- ', '1.', '你好。']);
  assert.deepEqual(chunks, ['你好。']);
});

test('cleanTTSText: keeps useful content (numbers / English words intact)', () => {
  const out = cleanTTSText('服务器返回了 HTTP 500 错误，请检查 API 的配置。');
  assert.equal(out, '服务器返回了 HTTP 500 错误，请检查 API 的配置。');
});

test('extractVoiceText: reads plain natural language with no <msg> dependency', () => {
  assert.equal(extractVoiceText('已经改完侧边栏，构建通过。'), '已经改完侧边栏，构建通过。');
});

test('extractVoiceText: drops progress <msg> blocks and prefers the final answer', () => {
  assert.equal(
    extractVoiceText('<msg>正在检查文件</msg><msg>准备修改</msg>已经检查完成。'),
    '已经检查完成。',
  );
});

test('extractVoiceText: report body wins when present', () => {
  assert.equal(
    extractVoiceText('<msg>正在检查文件</msg><report>已完成修改并通过构建。</report>'),
    '已完成修改并通过构建。',
  );
});

test('truncateAtSentence: keeps whole sentences inside the char budget', () => {
  const out = truncateAtSentence('第一句话在这里。第二句话也在这里。第三句话就被砍掉了。', 20);
  assert.ok(Array.from(out).length <= 20, out);
  assert.equal(out, '第一句话在这里。第二句话也在这里。');
});

test('truncateAtSentence: hard-truncates a single overlong sentence', () => {
  const out = truncateAtSentence('很长的一句话'.repeat(20), 30);
  assert.equal(Array.from(out).length, 30);
  assert.ok(out.endsWith('…'));
});

test('selectDialogueSpeechSegments: plain final text is spoken, no progress tags needed', () => {
  assert.deepEqual(
    selectDialogueSpeechSegments('<msg>正在检查文件</msg>已经检查完成。'),
    ['已经检查完成。'],
  );
});

test('selectDialogueSpeechSegments: final report is the only final utterance', () => {
  const text = '<msg>正在检查文件</msg><report>已经完成修改并通过构建。</report>';
  assert.deepEqual(
    selectDialogueSpeechSegments(text),
    ['已经完成修改并通过构建。'],
  );
});

test('selectDialogueSpeechSegments: long answers are capped by the char budget', () => {
  const long = Array.from({ length: 40 }, (_, i) => `这是第${i + 1}句完整的回复内容。`).join('');
  const segments = selectDialogueSpeechSegments(long);
  const total = segments.join('').replace(/。/g, '');
  assert.ok(Array.from(total).length <= 60, `too long: ${Array.from(total).length}`);
  assert.ok(segments.length >= 1);
});

test('selectDialogueSpeechSegments: a turn with only progress tags speaks nothing', () => {
  assert.deepEqual(selectDialogueSpeechSegments('<msg>正在处理</msg>'), []);
});

test('selectDialogueSpeechText: plain natural language falls through unchanged', () => {
  assert.equal(selectDialogueSpeechText('你好，主人。'), '你好，主人。');
});

test('clampDialogueText: flattens whitespace and keeps the dialog to two lines', () => {
  const out = clampDialogueText('第一行内容\n第二行内容 ' + '继续'.repeat(40));
  assert.ok(Array.from(out).length <= 64);
  assert.ok(!out.includes('\n'));
  assert.ok(out.endsWith('…'));
});
