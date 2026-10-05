"""MOSS-TTS-Nano-100M-ONNX inference engine (CPU, torch-free).

* Batch synthesis: prefill -> decode -> local_fixed_sampled_frame -> codec full decode.
* Streaming synthesis: AR frames are decoded incrementally via the codec
  decode_step graph and emitted as small audio chunks (low time-to-first-audio,
  doubao-style).  Use synthesize_stream().
* Voice cloning: encode a reference wav via moss_audio_tokenizer_encode.onnx
  into prompt audio codes, then use them as the voice prompt.
"""
import json, os, time, wave
import numpy as np
import onnxruntime as ort
import sentencepiece as spm


def _slice_channel_major_audio(audio, start_sample=0, end_sample=None):
    if audio.ndim != 3 or audio.shape[0] != 1:
        raise ValueError(f"Unexpected audio tensor shape: {audio.shape}")
    channels = int(audio.shape[1])
    total = int(audio.shape[2])
    start = max(0, int(start_sample))
    end = total if end_sample is None else max(start, min(int(end_sample), total))
    return [audio[0, c, start:end].astype(np.float32, copy=False) for c in range(channels)]


def _merge_audio_channels(channel_arrays):
    if not channel_arrays:
        return np.zeros((0, 1), dtype=np.float32)
    if len(channel_arrays) == 1:
        return np.asarray(channel_arrays[0], dtype=np.float32).reshape(-1, 1)
    min_len = min(int(ch.shape[0]) for ch in channel_arrays)
    trimmed = [np.asarray(ch[:min_len], dtype=np.float32) for ch in channel_arrays]
    return np.stack(trimmed, axis=1)


def _interleave_stereo(channel_major):
    """(2, n) float32 -> (2n,) float32 [L,R,L,R,...]"""
    if channel_major.shape[0] != 2:
        return np.ascontiguousarray(channel_major.reshape(-1), dtype=np.float32)
    n = channel_major.shape[1]
    out = np.empty(n * 2, dtype=np.float32)
    out[0::2] = channel_major[0]
    out[1::2] = channel_major[1]
    return out


class CodecStreamingDecodeSession:
    """Incremental codec decoder (official port). Decodes a few frames at a time."""

    def __init__(self, codec_meta, session):
        self.codec_meta = codec_meta
        self.session = session
        self.transformer_specs = list(codec_meta.get("streaming_decode", {}).get("transformer_offsets", []))
        self.attention_specs = list(codec_meta.get("streaming_decode", {}).get("attention_caches", []))
        self.state_feeds = {}
        self.reset()

    def reset(self):
        self.state_feeds = {}
        for spec in self.transformer_specs:
            self.state_feeds[str(spec["input_name"])] = np.zeros(tuple(spec["shape"]), dtype=np.int32)
        for spec in self.attention_specs:
            self.state_feeds[str(spec["offset_input_name"])] = np.zeros(tuple(spec["offset_shape"]), dtype=np.int32)
            self.state_feeds[str(spec["cached_keys_input_name"])] = np.zeros(tuple(spec["cache_shape"]), dtype=np.float32)
            self.state_feeds[str(spec["cached_values_input_name"])] = np.zeros(tuple(spec["cache_shape"]), dtype=np.float32)
            positions = np.full(tuple(spec["positions_shape"]), -1, dtype=np.int32)
            self.state_feeds[str(spec["cached_positions_input_name"])] = positions

    def run_frames(self, frame_rows):
        if not frame_rows:
            return None
        num_quantizers = int(self.codec_meta["codec_config"]["num_quantizers"])
        frame_count = len(frame_rows)
        audio_codes = np.zeros((1, frame_count, num_quantizers), dtype=np.int32)
        for fi, row in enumerate(frame_rows):
            for ci in range(num_quantizers):
                audio_codes[0, fi, ci] = int(row[ci] if ci < len(row) else 0)
        feeds = {
            "audio_codes": audio_codes,
            "audio_code_lengths": np.asarray([frame_count], dtype=np.int32),
        }
        feeds.update(self.state_feeds)
        outputs = self.session.run(None, feeds)
        names = [o.name for o in self.session.get_outputs()]
        named = dict(zip(names, outputs, strict=True))
        for spec in self.transformer_specs:
            self.state_feeds[str(spec["input_name"])] = named[str(spec["output_name"])]
        for spec in self.attention_specs:
            self.state_feeds[str(spec["offset_input_name"])] = named[str(spec["offset_output_name"])]
            self.state_feeds[str(spec["cached_keys_input_name"])] = named[str(spec["cached_keys_output_name"])]
            self.state_feeds[str(spec["cached_values_input_name"])] = named[str(spec["cached_values_output_name"])]
            self.state_feeds[str(spec["cached_positions_input_name"])] = named[str(spec["cached_positions_output_name"])]
        return named["audio"], int(named["audio_lengths"].reshape(-1)[0])


class MossTTSNanoEngine:
    def __init__(self, tts_dir, codec_dir, thread_count=4, seed=2):
        self.seed = int(seed)
        self._rng = np.random.default_rng(self.seed)
        self.tts_dir = tts_dir
        self.codec_dir = codec_dir
        with open(os.path.join(tts_dir, "browser_poc_manifest.json"), "r", encoding="utf-8") as f:
            m = json.load(f)
        with open(os.path.join(tts_dir, "tts_browser_onnx_meta.json"), "r", encoding="utf-8") as f:
            meta = json.load(f)
        with open(os.path.join(codec_dir, "codec_browser_onnx_meta.json"), "r", encoding="utf-8") as f:
            cm = json.load(f)

        self.meta = meta
        self.cm = cm
        cfg = m["tts_config"]
        self.audio_pad = int(cfg["audio_pad_token_id"])
        self.audio_start = int(cfg["audio_start_token_id"])
        self.audio_end = int(cfg["audio_end_token_id"])
        self.audio_user_slot = int(cfg["audio_user_slot_token_id"])
        self.audio_assistant_slot = int(cfg["audio_assistant_slot_token_id"])
        self.n_vq = int(cfg["n_vq"])
        self.vocab_size = int(cfg["vocab_size"])
        self.audio_codebook_total = sum(int(x) for x in cfg["audio_codebook_sizes"])
        self.mask_size = self.vocab_size + self.audio_codebook_total

        mc = meta["model_config"]
        self.global_layers = int(mc["global_layers"])
        self.row_width = int(mc["row_width"])
        self.codec_sr = int(cm["codec_config"]["sample_rate"])
        self.codec_channels = int(cm["codec_config"]["channels"])
        self.max_new_frames = int(m["generation_defaults"]["max_new_frames"])

        self.pt = [int(x) for x in m["prompt_templates"]["user_prompt_prefix_token_ids"]]
        self.par = [int(x) for x in m["prompt_templates"]["user_prompt_after_reference_token_ids"]]
        self.ap = [int(x) for x in m["prompt_templates"]["assistant_prompt_prefix_token_ids"]]
        self.builtin_pc = [[int(x) for x in row] for row in m["builtin_voices"][0]["prompt_audio_codes"]]
        self.pc = self.builtin_pc

        self.thread_count = max(1, int(thread_count))
        self._load_models()
        self._load_tokenizer()

    def _load_tokenizer(self):
        self.sp = spm.SentencePieceProcessor()
        self.sp.Load(os.path.join(self.tts_dir, "tokenizer.model"))

    def _create_session(self, path):
        opts = ort.SessionOptions()
        opts.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
        opts.intra_op_num_threads = self.thread_count
        opts.inter_op_num_threads = 1
        return ort.InferenceSession(path, opts, providers=["CPUExecutionProvider"])

    def _load_models(self):
        t = time.time()
        self.sess_prefill = self._create_session(os.path.join(self.tts_dir, "moss_tts_prefill.onnx"))
        self.sess_decode = self._create_session(os.path.join(self.tts_dir, "moss_tts_decode_step.onnx"))
        self.sess_fixed = self._create_session(os.path.join(self.tts_dir, "moss_tts_local_fixed_sampled_frame.onnx"))
        self.sess_codec = self._create_session(os.path.join(self.codec_dir, "moss_audio_tokenizer_decode_full.onnx"))
        self.sess_encode = self._create_session(os.path.join(self.codec_dir, "moss_audio_tokenizer_encode.onnx"))
        self.sess_codec_step = self._create_session(os.path.join(self.codec_dir, "moss_audio_tokenizer_decode_step.onnx"))
        self.codec_streaming = CodecStreamingDecodeSession(codec_meta=self.cm, session=self.sess_codec_step)
        self._load_sec = time.time() - t

    # ── voice cloning ────────────────────────────────────────────────
    def set_prompt_codes(self, codes):
        self.pc = [[int(x) for x in row] for row in codes]

    def encode_reference_audio(self, wav_path):
        """Load a wav (mono/stereo, any sample rate) -> prompt audio codes."""
        wav_path = str(wav_path)
        with wave.open(wav_path, "rb") as w:
            channels = w.getnchannels()
            sw = w.getsampwidth()
            sr = w.getframerate()
            n = w.getnframes()
            raw = w.readframes(n)
        if sw == 2:
            audio = np.frombuffer(raw, dtype="<i2").astype(np.float32) / 32768.0
        elif sw == 4:
            audio = np.frombuffer(raw, dtype="<i4").astype(np.float32) / 2147483648.0
        elif sw == 1:
            audio = (np.frombuffer(raw, dtype=np.uint8).astype(np.float32) - 128.0) / 128.0
        else:
            raise ValueError(f"unsupported sample width: {sw}")
        audio = audio.reshape(-1, channels).T  # (channels, n)
        if channels == 1:
            audio = np.repeat(audio, 2, axis=0)
        elif channels > 2:
            audio = audio[:2]
        # resample to codec sr (linear interpolation)
        if sr != self.codec_sr:
            n_out = int(round(audio.shape[1] * self.codec_sr / sr))
            x_old = np.linspace(0.0, 1.0, audio.shape[1], dtype=np.float64)
            x_new = np.linspace(0.0, 1.0, n_out, dtype=np.float64)
            audio = np.stack([
                np.interp(x_new, x_old, audio[c].astype(np.float64)).astype(np.float32)
                for c in range(audio.shape[0])
            ])
        audio = np.clip(audio, -1.0, 1.0).astype(np.float32)[None, :, :]  # (1, ch, n)
        outputs = self.sess_encode.run(None, {
            "waveform": audio,
            "input_lengths": np.asarray([audio.shape[2]], dtype=np.int32),
        })
        codes = np.asarray(outputs[0], dtype=np.int32)
        code_len = int(np.asarray(outputs[1]).reshape(-1)[0])
        return [[int(codes[0, fi, ci]) for ci in range(self.n_vq)] for fi in range(code_len)]

    # ── prompt building ──────────────────────────────────────────────
    def _build_request_rows(self, text_token_ids):
        prefix_ids = [*self.pt, self.audio_start]
        suffix_ids = [self.audio_end, *self.par, *text_token_ids, *self.ap, self.audio_start]
        rows = []
        for tok in prefix_ids:
            row = [self.audio_pad] * self.row_width
            row[0] = int(tok)
            rows.append(row)
        for code_row in self.pc:
            row = [self.audio_pad] * self.row_width
            row[0] = self.audio_user_slot
            for i in range(min(len(code_row), self.n_vq)):
                row[i + 1] = int(code_row[i])
            rows.append(row)
        for tok in suffix_ids:
            row = [self.audio_pad] * self.row_width
            row[0] = int(tok)
            rows.append(row)
        return rows

    def _prefill(self, rows):
        seq = len(rows)
        input_ids = np.array(rows, dtype=np.int32).reshape(1, seq, self.row_width)
        attention_mask = np.ones((1, seq), dtype=np.int32)
        o = self.sess_prefill.run(None, {"input_ids": input_ids, "attention_mask": attention_mask})
        h = o[0]
        if h.ndim == 3:
            h = h[:, -1, :]
        kv = {}
        for i in range(self.global_layers):
            kv[f"k{i}"] = o[1 + i * 2]
            kv[f"v{i}"] = o[2 + i * 2]
        return h, kv, seq

    def _sample_frame(self, h, previous_token_sets):
        rm = np.zeros((1, self.n_vq, 1024), dtype=np.int32)
        for ci, s in enumerate(previous_token_sets):
            for tok in s:
                if 0 <= tok < 1024:
                    rm[0, ci, tok] = 1
        o2 = self.sess_fixed.run(None, {
            "global_hidden": h.astype(np.float32, copy=False),
            "repetition_seen_mask": rm,
            "assistant_random_u": np.array([min(0.99999994, max(0.0, float(self._rng.random())))], dtype=np.float32),
            "audio_random_u": np.array([[min(0.99999994, max(0.0, float(self._rng.random()))) for _ in range(self.n_vq)]], dtype=np.float32),
        })
        should_continue = bool(int(np.asarray(o2[0]).reshape(-1)[0]))
        frame = [int(x) for x in np.asarray(o2[1]).reshape(-1)]
        return should_continue, frame

    def _decode_step(self, frame, kv, sl):
        next_row = np.full((1, 1, self.row_width), self.audio_pad, dtype=np.int32)
        next_row[0, 0, 0] = self.audio_assistant_slot
        for ci, tok in enumerate(frame):
            next_row[0, 0, ci + 1] = int(tok)
        feed = {"input_ids": next_row, "past_valid_lengths": np.array([sl], dtype=np.int32)}
        for i in range(self.global_layers):
            feed[f"past_key_{i}"] = kv[f"k{i}"]
            feed[f"past_value_{i}"] = kv[f"v{i}"]
        o = self.sess_decode.run(None, feed)
        for i in range(self.global_layers):
            kv[f"k{i}"] = o[1 + i * 2]
            kv[f"v{i}"] = o[2 + i * 2]
        h = o[0]
        if h.ndim == 3:
            h = h[:, -1, :]
        return h, kv, sl + 1

    def _generate_frames(self, text_token_ids, max_frames):
        rows = self._build_request_rows(text_token_ids)
        h, kv, sl = self._prefill(rows)
        previous_token_sets = [set() for _ in range(self.n_vq)]
        frames = []
        for _ in range(max_frames):
            should_continue, frame = self._sample_frame(h, previous_token_sets)
            if not should_continue:
                break
            frames.append(frame)
            for ci, tok in enumerate(frame):
                if 0 <= tok < 1024:
                    previous_token_sets[ci].add(tok)
            h, kv, sl = self._decode_step(frame, kv, sl)
        return frames

    # ── batch synthesis ──────────────────────────────────────────────
    def synthesize(self, text, max_frames=None, prompt_codes=None):
        if max_frames is None:
            max_frames = self.max_new_frames
        t0 = time.time()
        self._rng = np.random.default_rng(self.seed)
        old_pc = self.pc
        if prompt_codes is not None:
            self.pc = [[int(x) for x in row] for row in prompt_codes]
        try:
            ti = list(self.sp.EncodeAsIds(text))
            frames = self._generate_frames(ti, max_frames)
        finally:
            self.pc = old_pc
        if not frames:
            return np.zeros((self.codec_channels, 0), dtype=np.float32), self.codec_sr, time.time() - t0
        codes = np.expand_dims(np.array(frames, dtype=np.int32), 0)
        o = self.sess_codec.run(None, {
            "audio_codes": codes,
            "audio_code_lengths": np.array([codes.shape[1]], dtype=np.int32),
        })
        alen = int(o[1][0]) if o[1].ndim == 1 else int(o[1][0, 0])
        return o[0][0, :, :alen], self.codec_sr, time.time() - t0

    # ── streaming synthesis ──────────────────────────────────────────
    def synthesize_stream(self, text, max_frames=None, prompt_codes=None, chunk_frames=8, on_chunk=None):
        """True streaming synthesis: AR frames and codec decode are interleaved.

        Every chunk_frames generated frames are decoded right away and emitted
        as an audio chunk, so the first audio arrives long before the sentence
        finishes.  on_chunk(wav_interleaved, sr) is called per chunk.
        Returns total generated frame count (or (frames, chunks) if no callback).
        """
        if max_frames is None:
            max_frames = self.max_new_frames
        t0 = time.time()
        self._rng = np.random.default_rng(self.seed)
        old_pc = self.pc
        if prompt_codes is not None:
            self.pc = [[int(x) for x in row] for row in prompt_codes]
        try:
            ti = list(self.sp.EncodeAsIds(text))
            rows = self._build_request_rows(ti)
            h, kv, sl = self._prefill(rows)
            previous_token_sets = [set() for _ in range(self.n_vq)]
            self.codec_streaming.reset()
            chunk = max(1, int(chunk_frames))
            pending = []
            total_frames = 0
            collected = [] if on_chunk is None else None
            for _ in range(max_frames):
                should_continue, frame = self._sample_frame(h, previous_token_sets)
                if not should_continue:
                    break
                pending.append(frame)
                total_frames += 1
                for ci, tok in enumerate(frame):
                    if 0 <= tok < 1024:
                        previous_token_sets[ci].add(tok)
                h, kv, sl = self._decode_step(frame, kv, sl)
                if len(pending) >= chunk:
                    self._emit_codec_chunk(pending, on_chunk, collected)
                    pending = []
            if pending:
                self._emit_codec_chunk(pending, on_chunk, collected)
            self.codec_streaming.reset()
        finally:
            self.pc = old_pc
        if on_chunk is not None:
            return total_frames
        return total_frames, collected

    def _emit_codec_chunk(self, frame_rows, on_chunk, collected):
        decoded = self.codec_streaming.run_frames(frame_rows)
        if decoded is None:
            return
        audio, alen = decoded
        if alen <= 0:
            return
        merged = _merge_audio_channels(_slice_channel_major_audio(audio, 0, alen))
        if merged.shape[0] <= 0:
            return
        merged = np.ascontiguousarray(merged)
        if merged.shape[1] == 2:
            interleaved = _interleave_stereo(merged.T)
        else:
            interleaved = merged.reshape(-1).astype(np.float32)
        if on_chunk is not None:
            on_chunk(interleaved, self.codec_sr)
        elif collected is not None:
            collected.append(interleaved)

