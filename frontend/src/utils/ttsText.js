// TTS 文本处理：与 App.jsx 共用同一份真实逻辑（可被测试直接导入）。
// 抽出来的目的：保证"发给 TTS 的分句/清洗/标题合并"是可验证的真实实现，
// 而不是测试里的复刻。

// 按句号 + 换行分句（！？留在句内）；长尾自动冲刷，避免流式积压。
export function splitSentences(acc) {
  const sentences = [];
  const re = /[^。\n]+[。\n]+/g;
  let m, last = 0;
  while ((m = re.exec(acc))) {
    sentences.push(m[0].trim());
    last = m.index + m[0].length;
  }
  let tail = String(acc).slice(last);
  if (tail.length > 80) {
    sentences.push(tail.trim());
    tail = '';
  }
  return { sentences: sentences.filter(Boolean), remainder: tail };
}

// 清洗：去 cite/标签/代码块/链接/编号/列表符/中文标点前空格/HTML 实体等。
export function cleanTTSText(text) {
  return String(text || '')
    .replace(/<cite[\s\S]*?<\/cite>/gi, ' ')
    .replace(/<[^>]+>/g, ' ')
    .replace(/```[\s\S]*?```/g, ' ')
    .replace(/\[(?:详情|来源|查看更多|点击查看|了解更多|阅读全文|链接)\]\([^)]*\)/gi, ' ')
    .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
    .replace(/https?:\/\/[^\s)>\]\}]+/gi, ' ')
    .replace(/\[\d+\]/g, ' ')
    .replace(/^\d+[.、)）]\s*/gm, '')
    .replace(/^[-*•]\s+/gm, '')
    .replace(/\s*[-*•]\s+/g, ' ')
    .replace(/^#{1,6}[ \t]*/gm, '')
    .replace(/[—–]|&[a-zA-Z#0-9]+;/g, ' ')
    .replace(/[\[\]#*_>`~|]/g, ' ')
    .replace(/\s+([，。：；！？、）】」』])/g, '$1')
    .replace(/\s+/g, ' ')
    .trim();
}

// 纯符号/编号残留（如 "-"、"1."）不应单独发给 TTS。
export function isArtifactOnly(text) {
  return /^[\s\-*•#\d.、)）]+$/.test(text);
}

export const MAX_VOICE_MSGS = 3;
export const MAX_VOICE_MSG_CHARS = 20;
export const DIALOGUE_MAX_CHARS = 64;

function truncateRunes(text, limit) {
  const runes = Array.from(String(text || ''));
  if (runes.length <= limit) return runes.join('');
  if (limit <= 1) return '…';
  return runes.slice(0, limit - 1).join('') + '…';
}

function stripInternalBlocks(text) {
  return String(text || '')
    .replace(/<(?:think|thinking|mm:think)(?:\s[^>]*)?>[\s\S]*?<\/(?:think|thinking|mm:think)>/gi, ' ')
    .replace(/<reflection(?:\s[^>]*)?>[\s\S]*?<\/reflection>/gi, ' ');
}

export function extractVoiceMsgs(
  text,
  maxMsgs = MAX_VOICE_MSGS,
  maxChars = MAX_VOICE_MSG_CHARS,
) {
  const out = [];
  const seen = new Set();
  const re = /<msg(?:\s[^>]*)?>([\s\S]*?)<\/msg>/gi;
  let match;
  while ((match = re.exec(String(text || '')))) {
    const clean = cleanTTSText(match[1]);
    if (!clean) continue;
    const short = truncateRunes(clean, maxChars);
    if (seen.has(short)) continue;
    seen.add(short);
    out.push(short);
    if (out.length >= maxMsgs) break;
  }
  return out;
}

// Build the segments used by TTS and the voice dialog. A final answer or
// report wins; otherwise progress turns keep a few <=20-character <msg>
// snippets as separate utterances. This prevents one long joined sentence and
// avoids repeating progress text in the final answer.
export function selectDialogueSpeechSegments(text) {
  const raw = stripInternalBlocks(text);
  const msgs = extractVoiceMsgs(raw);
  const withoutMsgs = raw.replace(/<msg(?:\s[^>]*)?>[\s\S]*?<\/msg>/gi, ' ');
  const report = withoutMsgs.match(/<report(?:\s[^>]*)?>([\s\S]*?)<\/report>/i);
  const finalText = cleanTTSText(report ? report[1] : withoutMsgs);
  if (finalText) return [finalText];
  return msgs;
}

export function selectDialogueSpeechText(text) {
  return selectDialogueSpeechSegments(text).join('。').trim();
}

// The PNG dialog only has room for two lines. CSS also clamps, but truncating
// here keeps React state and TTS captions from carrying invisible overflow.
export function clampDialogueText(text, maxChars = DIALOGUE_MAX_CHARS) {
  const clean = String(text || '').replace(/\s+/g, ' ').trim();
  return truncateRunes(clean, maxChars);
}

// 由 splitSentences 产出的原始句子构建最终 TTS chunk：
// 清洗 -> 丢弃纯符号残留 -> 标题碎片（以 ：结尾）与下一句合并。
export function buildTTSChunks(sentences) {
  const chunks = [];
  let pending = '';
  for (const s of sentences) {
    const c = cleanTTSText(s);
    if (!c) continue;
    if (isArtifactOnly(c)) continue;
    const merged = pending + c;
    if (/[:：]$/.test(merged)) { pending = merged; continue; }
    chunks.push(merged);
    pending = '';
  }
  if (pending) chunks.push(pending);
  return chunks;
}
