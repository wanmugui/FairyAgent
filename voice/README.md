# Fairy Voice Service (TTS / STT)

`voice_service.py` serves:
- HTTP `:8787`
  - `POST /api/tts` — batch text->wav (float32 b64, whole sentence at once)
  - `POST /api/tts/stream` — **SSE streaming TTS** (doubao-style): audio chunks
    (`{"type":"audio","sample_rate":48000,"channels":2,"audio_b64":...}`) are
    pushed as the model decodes them, so playback starts ~0.6-1s into a sentence
    instead of waiting for full synthesis. Ends with `{"type":"done",...}`.
  - `GET /health` — includes `voice` (builtin-junhao / fairy-custom) + frame count
- WebSocket `:8788` — streaming STT (Faster-Whisper)

The `/voice` page runs this WebSocket in wake-word mode:
- always-on ambient listening while the microphone session is active;
- the voice page starts continuous listening automatically; the browser may
  ask once for microphone permission;
- a wake word switches the session from waiting to active conversation;
- wake phrases include English `fairy` and Chinese near-homophones such as
  `绯蕊 / 菲蕊 / 绯瑞 / 菲瑞 / 飞蕊 / 菲雅`;
- after wake-up, a command is collected until ~2.0 s of silence, then sent to
  the agent; short silence keeps the conversation active;
- a single command can run for up to 20 s before the safety endpoint fires;
- 30 s of silence resets the session to wake-word-required mode;
- STT does **not** pause during `thinking` or `speaking`. A new voice command
  is injected into the running turn via `/api/chat/inject`; if the agent is
  already speaking, TTS is stopped and a fresh turn starts immediately.

The main page streams replies to TTS by sentence-splitting the assistant text and
POSTing each completed sentence to `/voice/api/tts/stream` (Vite proxy -> :8787),
playing the SSE chunks back-to-back for gapless audio.

## Custom voice (voice cloning)
Place a reference wav (any sample rate, mono/stereo) at the repo root as
`fairy_(Vocals).wav` (or point `FAIRY_REFERENCE_WAV` at another path). On first
request the service encodes it via `moss_audio_tokenizer_encode.onnx` into prompt
audio codes and uses them as the voice; `GET /health` then reports
`"voice":"fairy-custom"`.

## 1. Install deps

```bash
pip install -r voice/requirements.txt
pip install -r voice/requirements-stt.txt
```

The STT dependency (`faster-whisper` / CTranslate2) needs a Python version
which has a wheel. If the project venv cannot install it, `pnpm dev` will
automatically reuse a Conda environment from
`~/.conda/environments.txt` that already contains `faster_whisper`.
You can also point it at an explicit interpreter:

```bash
set FAIRY_VOICE_PYTHON=D:\Anaconda\envs\GPTSoVits\python.exe
```

Faster-Whisper downloads the `base` model on first use. To keep it fully
offline, place a converted model under
`models/voice/faster-whisper-base/` or set `FAIRY_STT_MODEL`:

Fairy's prebuilt STT model is also stored in ModelScope alongside TTS:
`wanmugui/Fairy-TTS-v2-ONNX` → `stt/faster-whisper-base/`.

```bash
set FAIRY_STT_MODEL=D:\models\faster-whisper-base
```

Optional tuning: `FAIRY_STT_LANGUAGE=zh`, `FAIRY_STT_DEVICE=cpu`,
`FAIRY_STT_COMPUTE_TYPE=int8`, `FAIRY_STT_CPU_THREADS=4`,
`FAIRY_STT_PARTIAL_INTERVAL=0.75`. Set `FAIRY_STT_LANGUAGE=auto` for mixed
Chinese/English speech.

`FAIRY_STT_COALESCE_SEC` defaults to `0.9`: partial decoding waits until this
much new audio has accumulated, reducing repeated Whisper encoder passes.

Streaming partial now defaults to `sherpa-onnx`:

- `FAIRY_STT_PARTIAL_BACKEND=sherpa` uses the local Zipformer for live partial text
- `FAIRY_STT_PARTIAL_BACKEND=whisper` forces the old Faster-Whisper partial path
- `FAIRY_SHERPA_MODEL_DIR` overrides the bundled model directory
- `FAIRY_SHERPA_NUM_THREADS=1` keeps CPU usage low by default

On this machine the 480 ms int8 Zipformer uses about **285 MB RSS** after load
and decodes a 10 s test clip with about **0.66 s CPU time**. The larger
Faster-Whisper model is still used for the final transcript.

For named entities, put one term per line in `config/stt_hotwords.txt` (comments
start with `#`) or set `FAIRY_STT_HOTWORDS=term1, term2`. Hotwords are passed to
both partial and final decoding. For a two-pass setup, keep the small model for
live partial text and set `FAIRY_STT_FINAL_MODEL` to a larger faster-whisper
model; the final pass then re-decodes the whole utterance for accuracy.

TTS first-packet tuning: `FAIRY_TTS_CHUNK_FRAMES` defaults to `4`. Lower values
start playback earlier but increase codec-step overhead; `2-6` is the practical
range.

By default lightweight pseudo-streaming is enabled
(`FAIRY_STT_PARTIAL_ENABLED=1`): partial text replaces the previous display
result directly, without common-prefix trimming. Only the final text is sent
to the agent. VAD, short-utterance windows and repetition suppression reduce
Whisper hallucinations.

## 2. Download ONNX models into `models/voice/`

ModelScope (recommended in CN):
```bash
pip install modelscope
modelscope download --model openmoss/MOSS-TTS-Nano-100M-ONNX --local_dir models/voice/MOSS-TTS-Nano-100M-ONNX
modelscope download --model openmoss/MOSS-Audio-Tokenizer-Nano-ONNX --local_dir models/voice/MOSS-Audio-Tokenizer-Nano-ONNX
```

HuggingFace:
```bash
pip install huggingface-hub
huggingface-cli download OpenMOSS-Team/MOSS-TTS-Nano-100M-ONNX --local-dir models/voice/MOSS-TTS-Nano-100M-ONNX
huggingface-cli download OpenMOSS-Team/MOSS-Audio-Tokenizer-Nano-ONNX --local-dir models/voice/MOSS-Audio-Tokenizer-Nano-ONNX
```

Note: the audio-tokenizer repo also needs the encode + decode_step ONNX files
(downloaded together by the commands above); the service requires
`moss_audio_tokenizer_encode.{onnx,data}` for voice cloning and
`moss_audio_tokenizer_decode_step.onnx` for streaming decode.

## 3. Start the service

```bash
python voice/voice_service.py
```

Verify: `curl http://127.0.0.1:8787/health` -> `{"ok":true,"voice":"fairy-custom",...}`

Streaming smoke test:
```bash
curl -N -X POST http://127.0.0.1:8787/api/tts/stream -H "Content-Type: application/json" -d '{"text":"你好，欢迎使用仙女助手。"}'
```

TTS playback on the main page (`http://127.0.0.1:5173/`) is enabled via `TTS_ENABLED`
in `frontend/src/App.jsx` (already true). Use the speaker button in the header to mute.
