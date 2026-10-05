import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  splitSentences,
  cleanTTSText,
  buildTTSChunks,
  isArtifactOnly,
  extractVoiceMsgs,
  selectDialogueSpeechText,
  selectDialogueSpeechSegments,
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

test('extractVoiceMsgs: keeps at most three unique messages, each <=20 chars', () => {
  const raw = [
    '<msg>我正在读取侧边栏里面的代码文件内容并准备修改</msg>',
    '<msg>我正在读取侧边栏里面的代码文件内容并准备修改</msg>',
    '<msg>接下来修改菜单展开逻辑</msg>',
    '<msg>然后运行前端构建测试</msg>',
    '<msg>第四条不应该被选中</msg>',
    '<msg>最后一条也不应该被选中</msg>',
  ].join('');
  const msgs = extractVoiceMsgs(raw);
  assert.equal(msgs.length, 3);
  for (const msg of msgs) {
    assert.ok(Array.from(msg).length <= 20, msg);
  }
  assert.ok(msgs[0].endsWith('…'));
  assert.equal(msgs[1], '接下来修改菜单展开逻辑');
  assert.equal(msgs[2], '然后运行前端构建测试');
});

test('selectDialogueSpeechText: final report wins over progress messages', () => {
  const text = '<msg>正在检查文件</msg><msg>准备修改</msg><report>已经完成修改并通过构建。</report>';
  assert.equal(
    selectDialogueSpeechText(text),
    '已经完成修改并通过构建。',
  );
});

test('selectDialogueSpeechText: uses short progress messages before final delivery', () => {
  const text = '<msg>正在检查文件</msg><msg>准备修改</msg>';
  assert.equal(selectDialogueSpeechText(text), '正在检查文件。准备修改');
});

test('selectDialogueSpeechSegments: progress messages stay separate TTS utterances', () => {
  const text = '<msg>正在检查文件</msg><msg>准备修改</msg>';
  assert.deepEqual(
    selectDialogueSpeechSegments(text),
    ['正在检查文件', '准备修改'],
  );
});

test('selectDialogueSpeechSegments: final report is the only final utterance', () => {
  const text = '<msg>正在检查文件</msg><report>已经完成修改并通过构建。</report>';
  assert.deepEqual(
    selectDialogueSpeechSegments(text),
    ['已经完成修改并通过构建。'],
  );
});

test('selectDialogueSpeechText: plain final text wins over progress tags', () => {
  const text = '<msg>正在检查文件</msg>已经检查完成。';
  assert.equal(selectDialogueSpeechText(text), '已经检查完成。');
});

test('selectDialogueSpeechText: falls back to plain natural language', () => {
  assert.equal(selectDialogueSpeechText('你好，主人。'), '你好，主人。');
});

test('clampDialogueText: flattens whitespace and keeps the dialog to two lines', () => {
  const out = clampDialogueText('第一行内容\n第二行内容 ' + '继续'.repeat(40));
  assert.ok(Array.from(out).length <= 64);
  assert.ok(!out.includes('\n'));
  assert.ok(out.endsWith('…'));
});
