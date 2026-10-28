/**
 * Model manifest.
 *
 * Every model the runtime loads on demand, with where to get it. Kept as data
 * so adding a model is a one-line change and so fetch-models.mjs has no
 * hard-coded knowledge to drift out of sync.
 *
 * Two shapes:
 *   - files: download each file into the directory. Used for the HuggingFace
 *     repos, which serve plain files.
 *   - archive: download one tarball and unpack it. Used for sherpa-onnx, whose
 *     GitHub release ships .tar.bz2 only.
 *
 * `required: false` means the feature degrades or errors when the model is
 * missing but the app still runs, so a failure to download must not block the
 * start. Every model here is loaded lazily today, which is why none of them
 * are required: the manifest keeps that choice explicit rather than implied.
 */

/** Where the runtime looks for models. */
export const TOOLS_DIR = ".tools";
export const MODELS_DIR = "models/voice";

export const MODELS = [
  {
    id: "faster-whisper-base",
    dir: `${MODELS_DIR}/faster-whisper-base`,
    title: "本地语音识别（faster-whisper base）",
    source: "HuggingFace · Systran/faster-whisper-base",
    homepage: "https://huggingface.co/Systran/faster-whisper-base",
    approxBytes: 147_881_823,
    files: [
      { name: "model.bin", bytes: 145_217_532 },
      { name: "config.json", bytes: 2_309 },
      { name: "tokenizer.json", bytes: 2_203_239 },
      { name: "vocabulary.txt", bytes: 459_861 },
    ],
    downloadUrl: (repo, file) =>
      `https://huggingface.co/${repo}/resolve/main/${file}`,
    repos: ["Systran/faster-whisper-base"],
  },
  {
    id: "moss-audio-tokenizer-nano",
    dir: `${MODELS_DIR}/MOSS-Audio-Tokenizer-Nano-ONNX`,
    title: "音频编解码器（MOSS Audio Tokenizer Nano, ONNX）",
    source: "HuggingFace · OpenMOSS-Team/MOSS-Audio-Tokenizer-Nano-ONNX",
    homepage: "https://huggingface.co/OpenMOSS-Team/MOSS-Audio-Tokenizer-Nano-ONNX",
        approxBytes: 90571586,
    files: [
      { name: "codec_browser_onnx_meta.json", bytes: 16461 },
      { name: "moss_audio_tokenizer_decode_full.onnx", bytes: 681902 },
      { name: "moss_audio_tokenizer_decode_shared.data", bytes: 44198912 },
      { name: "moss_audio_tokenizer_decode_step.onnx", bytes: 351400 },
      { name: "moss_audio_tokenizer_encode.data", bytes: 44507136 },
      { name: "moss_audio_tokenizer_encode.onnx", bytes: 815775 },
    ],
    downloadUrl: (repo, file) =>
      `https://huggingface.co/${repo}/resolve/main/${file}`,
    repos: ["OpenMOSS-Team/MOSS-Audio-Tokenizer-Nano-ONNX"],
  },
  {
    id: "moss-tts-nano",
    dir: `${MODELS_DIR}/MOSS-TTS-Nano-100M-ONNX`,
    title: "本地语音合成（MOSS TTS Nano 100M, ONNX）",
    source: "HuggingFace · OpenMOSS-Team/MOSS-TTS-Nano-100M-ONNX",
    homepage: "https://huggingface.co/OpenMOSS-Team/MOSS-TTS-Nano-100M-ONNX",
        approxBytes: 672516589,
    files: [
      { name: "browser_poc_manifest.json", bytes: 503354 },
      { name: "moss_tts_decode_step.onnx", bytes: 291483 },
      { name: "moss_tts_global_shared.data", bytes: 440813568 },
      { name: "moss_tts_local_fixed_sampled_frame.onnx", bytes: 471414 },
      { name: "moss_tts_local_shared.data", bytes: 229678080 },
      { name: "moss_tts_prefill.onnx", bytes: 283305 },
      { name: "tokenizer.model", bytes: 470897 },
      { name: "tts_browser_onnx_meta.json", bytes: 4488 },
    ],
    downloadUrl: (repo, file) =>
      `https://huggingface.co/${repo}/resolve/main/${file}`,
    repos: ["OpenMOSS-Team/MOSS-TTS-Nano-100M-ONNX"],
  },
  {
    id: "sherpa-x-asr",
    dir: `${TOOLS_DIR}/sherpa-models/sherpa-onnx-x-asr-480ms-streaming-zipformer-transducer-zh-en-punct-int8-2026-06-05`,
    title: "流式语音识别（sherpa-onnx x-asr transducer）",
    source: "GitHub Releases · k2-fsa/sherpa-onnx",
    homepage: "https://github.com/k2-fsa/sherpa-onnx/releases/tag/asr-models",
    approxBytes: 171_174_794,
    archive: {
      url:
        "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/" +
        "sherpa-onnx-x-asr-480ms-streaming-zipformer-transducer-zh-en-punct-int8-2026-06-05.tar.bz2",
      // The tarball unpacks into a directory of this name; it becomes the
      // model directory directly.
      topLevel:
        "sherpa-onnx-x-asr-480ms-streaming-zipformer-transducer-zh-en-punct-int8-2026-06-05",
    },
    // Unpacked contents, used to decide whether the model is already there.
    files: [
      { name: "encoder.int8.onnx", bytes: 155_278_641 },
      { name: "decoder.onnx", bytes: 11_309_084 },
      { name: "joiner.int8.onnx", bytes: 2_581_422 },
      { name: "bpe.model", bytes: 119_265 },
      { name: "tokens.txt", bytes: 58_806 },
      { name: "README.md", bytes: 96 },
      { name: "test_onnx.py", bytes: 7_262 },
      { name: "test_wavs/0.wav", bytes: 321_744 },
      { name: "test_wavs/1.wav", bytes: 163_244 },
      { name: "test_wavs/2.wav", bytes: 150_124 },
      { name: "test_wavs/3.wav", bytes: 282_604 },
    ],
  },
];

export function formatBytes(bytes) {
  if (!bytes) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
}
