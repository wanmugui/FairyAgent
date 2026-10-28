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

// 语音朗读不再依赖 <msg> 标签：直接取 text 类型的自然语言回复，
// 并用字数上限保证一轮不会念成长篇报告。
export const MAX_VOICE_TEXT_CHARS = 60;
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

// 取"可以直接朗读"的自然语言正文：<report> 优先，其次去掉 <msg>
// 与内部思考块后的普通文本。返回空串表示这一轮没有可朗读的正文。
export function extractVoiceText(text) {
  const raw = stripInternalBlocks(text)
    .replace(/<msg(?:\s[^>]*)?>[\s\S]*?<\/msg>/gi, ' ')
    .replace(/<\/?msg(?:\s[^>]*)?>/gi, ' ');
  const report = raw.match(/<report(?:\s[^>]*)?>([\s\S]*?)<\/report>/i);
  return cleanTTSText(report ? report[1] : raw);
}

// 按句号/换行优先在字数上限内截断，句子读起来才完整；没有句号时按字符硬截。
export function truncateAtSentence(text, maxChars) {
  const clean = String(text || '').trim();
  if (Array.from(clean).length <= maxChars) return clean;
  const sentences = splitSentences(clean).sentences;
  const kept = [];
  let used = 0;
  for (const s of sentences) {
    const len = Array.from(s).length;
    if (used + len > maxChars) break;
    kept.push(s.trim());
    used += len;
  }
  if (kept.length) return kept.join('');
  return truncateRunes(clean, maxChars);
}

// 一轮语音的朗读片段：自然语言正文按句切分，整体不超过字数上限。
export function selectDialogueSpeechSegments(text, maxChars = MAX_VOICE_TEXT_CHARS) {
  const clean = extractVoiceText(text);
  if (!clean) return [];
  return truncateAtSentence(clean, maxChars)
    .split('\n')
    .map(s => s.trim())
    .filter(Boolean);
}

export function selectDialogueSpeechText(text, maxChars = MAX_VOICE_TEXT_CHARS) {
  return selectDialogueSpeechSegments(text, maxChars).join('。').trim();
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
