# Windows Computer Broker

Fairy's `computer_*` desktop tools talk to a small C# broker instead of the
PowerShell bridge that used to sit in
`agent/internal/biz/tool/local/computer_windows.ps1`.

## Why the PowerShell bridge was not enough

On a display scaled above 100% three different coordinate systems are in play:

| Space | Example on a 3840x2160 panel at 150% |
| --- | --- |
| Physical screen pixels | `3840 x 2160` |
| Window rectangle reported by `GetWindowRect` | includes the invisible DWM resize border |
| Cursor position | physical pixels, but only from a DPI-aware process |

A non DPI-aware PowerShell host saw `2560 x 1440`, opened screenshots at that
size, and then fed those numbers to `SendInput`. The click always landed at the
wrong physical pixel and `SetCursorPos` silently failed on protected windows, so
nothing could be verified. That is the bug this broker removes.

## What the broker does

1. **Per-Monitor-V2 DPI awareness** is set before any Win32/UI call
   (`SetProcessDpiAwarenessContext(PER_MONITOR_AWARE_V2)`, falling back to
   `SetProcessDpiAwareness` then `SetProcessDPIAware`).
2. **One coordinate space.** Screenshots are captured 1:1 into physical pixels
   and report `screen_origin` + `scale`. Window rectangles use
   `DwmGetWindowAttribute(DWMWA_EXTENDED_FRAME_BOUNDS)` so the invisible border is
   excluded.
3. **Verified pointer movement.** After `SendInput` the cursor is read back and
   compared with the request; `SetCursorPos` is a fallback. The result carries
   `cursor_before`, `cursor_after`, `delta_x`, `delta_y` and `verified`.
4. **Stale coordinate detection.** `screenshot` returns a `geometry_hash`.
   Actions accept `expected_geometry_hash` and fail with `stale_coordinate` when
   the desktop geometry changed in between.
5. **Verified clicks.** `image_diff` compares a before/after pair, optionally
   restricted to a `region`, and returns the changed ratio and bounding box.
6. **UI Automation first.** `ui_tree` and `invoke_element` reach controls through
   UIA; raw input is the fallback for controls that expose no pattern.

## Coordinate spaces

`coordinate_space` selects how `x`/`y` are interpreted:

| Value | Meaning |
| --- | --- |
| `screen` (default) | physical desktop pixels |
| `screenshot` | offset by the virtual desktop origin (`screen_origin`) |
| `window` | relative to the window's DWM frame, needs `handle` |
| `client` | relative to the window's client area, needs `handle` |

## Protocol

One JSON object on stdin, one `__FAIRY_CUA_RESULT__` prefixed JSON line on
stdout. The shape matches the old PowerShell bridge, so `computer.go` can fall
back to either.

```powershell
'{"tool":"computer_observe","action":"screen_info"}' | .\FairyComputerBroker.exe
```

### Tools and actions

| Tool | Actions |
| --- | --- |
| `computer_observe` | `screen_info`, `cursor_position`, `screenshot`, `image_diff`, `active_window`, `list_windows`, `window_info`, `ui_tree` |
| `computer_pointer` | `move`, `click`, `double_click`, `right_click`, `drag`, `scroll`, `mouse_down`, `mouse_up`, `invoke_element` |
| `computer_keyboard` | `type` (Unicode or clipboard), `press`, `hotkey`, `key_down`, `key_up` |
| `computer_window` | `open`, `activate`, `minimize`, `maximize`, `restore`, `close`, `move`, `resize`, `frame_bounds`, `find` |
| `computer_clipboard` | `get`, `set`, `clear` |

## Build

The Go tool embeds `agent/internal/biz/tool/local/computer_broker.cs` and compiles
it on demand into `<repo>\.tools\computer-broker\`, caching by source hash. No
extra runtime is installed: `csc.exe` ships with .NET Framework 4.x on Windows
10/11, and the resulting binary is ~40 KB.

To iterate on the C# source without starting the agent:

```powershell
powershell -ExecutionPolicy Bypass -File tools\computer-broker\build-broker.ps1
```

## Regression test

The smoke test performs a real click on Notepad's File menu and only reports
success when the screenshot actually changed:

```powershell
powershell -ExecutionPolicy Bypass -File tools\computer-broker\smoke-test.ps1
```

Artifacts (`before.png`, `after.png`, `diff.png`) are written to
`workspace\result\computer-broker-smoke\<timestamp>\`.

## Boundaries

The broker uses documented, ordinary-user-level APIs only. It does not elevate,
inject into other processes, or bypass anti-cheat or DRM. Applications that
deliberately reject synthetic input (some protected games) will still ignore
clicks; that is a platform boundary, not a bug in the broker.

---

# Vision path (OCR + YOLO)

UI Automation covers standard desktop controls. It cannot see games, Electron
and Flutter canvas, remote desktop sessions, or video. `computer_observe` gains
pixel actions for those cases, served by a local ONNX service instead of the
broker:

| Action | Purpose |
| --- | --- |
| `vision_start` | open a real-time observation session (keeps models and the capture buffer warm) |
| `vision_stop` | end it and let the process exit |
| `vision_status` | session state, warm models, cache hit counters |
| `find_text` | locate a label from pixels, returns a screen point you can click |
| `locate_text` | same, tolerant of approximate labels |
| `ocr` | every text block with boxes |
| `detect_elements` | text plus YOLO icon boxes, merged and numbered |

## Lifecycle

The service is **not resident by default**. The agent decides when it wants a
pixel pipeline:

* first vision action -> the Go tool compiles/launches the service and waits for health
* while a `vision_start` session is open -> it stays resident regardless of traffic
* after `vision_stop`, or once no session is open and the idle timeout expires -> it exits
* the child is told the agent's PID, so it also exits if the agent dies

## Measured on this machine (i7-class laptop, Windows 11, 4K at 150%)

| Case | Cold | Repeat (cached) |
| --- | --- | --- |
| Full 3840x2160 screen, 120 text boxes | ~5.0 s | ~15 ms |
| Notepad window region, 23 text boxes | ~0.7-1.9 s | ~16 ms |

Two things dominate, and both are handled:

1. **Cost scales with the number of text boxes**, because recognition runs per
   box. A full desktop has ~120 of them.
2. **The expensive unit is one pass per screen state, not per question.** Results
   are cached by frame signature, so an agent that asks "where is X", then
   "where is Y" on the same frame pays once. Combined with the frame-change gate,
   a static screen costs nothing.

`region` is the other lever: analysing a window frame instead of the whole
desktop is worth roughly 3x.

## What did not work

Recorded so the next person does not repeat it:

* **Naive tile/thread parallelism was a 3x regression** (22.9 s vs 6.9 s for the
  same frame). ONNX Runtime sessions default to one thread per core, so N workers
  oversubscribe the CPU. Doing it properly requires constructing sessions with
  `intra_op_num_threads` capped, which RapidOCR does not expose cleanly.
* Raising `rec_batch_num` was slower (7.3 s vs 5.2 s).
* Capping the detector's long side barely moved the total, because detection is
  the smaller half.
* The detector's default `limit_type='min'` never shrinks an image whose short
  side already exceeds the limit, so a 4K screen was being detected at full
  resolution. Switching to `limit_type='max'` is still correct, it is just not
  where the time went.

## Regression test

```powershell
powershell -ExecutionPolicy Bypass -File tools\computer-broker\vision-smoke-test.ps1
```

It takes the label and rectangle of a control from UI Automation as ground
truth, asks OCR to find the same label from pixels, asserts the OCR point lands
inside the UIA rectangle, clicks it, and proves the click changed the screen.
That cross-check is what caught two real defects: a fuzzy match being reported
as a confident hit, and region-relative coordinates leaking into a click.

## Accuracy guard

`find_text` / `locate_text` apply `min_ratio` (default 0.6). Below it they return
`found: false` with `reason: no_confident_match` plus the closest candidates. A
confident wrong answer is worse than "not found", because it makes the agent
click the wrong control.

## Two failures worth remembering

**Minimized windows report (-32000, -32000).** Win32 parks a minimized window at
that sentinel with a tiny size. The broker used to hand it back as a normal
`frame`, so a caller that passed it as a `region` cropped empty space and then
reported "no text found" - which points the investigation at OCR instead of at
the minimized window. Now:

* `frame_valid` is reported and is `false` for a minimized window
* `activate` polls until the restore actually lands, and returns the usable
  geometry in the same response
* `screenshot` with a `handle` refuses outright rather than capturing nothing

The regression test also had the bug: it read `frame` from `find` *before*
activating, so it inherited the sentinel. It now re-reads the frame from the
activation result. A test that consumes stale geometry tests nothing.

**An exact frame hash makes the cache useless.** A blinking text caret, a
taskbar clock or any 1px animation changes a few pixels, so every lookup misses
and the "one OCR pass per screen state" design never pays off. The signature is
now a 32x18 greyscale fingerprint compared with a 2% tolerance:

```
cold lookup   1384 ms
repeat lookup   10 ms      (with a blinking caret on screen)
```

Real UI changes still alter enough cells to invalidate it; a caret does not.

## Camera

`source: "camera"` plus `camera_index` swaps the frame source; the rest of the
pipeline is unchanged because both paths produce a numpy BGR frame. This is
scaffolding only and has not been tested against real hardware.

## YOLO detection models

`detect_elements` and `locate` use a YOLO ONNX model when one is installed. Two
already-trained models are supported; nothing here is trained locally.

| Model | Classes | ONNX | 4K inference |
| --- | --- | --- | --- |
| `ui-elements-detection` (YOLO11-L, MacPaw) | `AXButton`, `AXDisclosureTriangle`, `AXImage`, `AXLink`, `AXTextArea` | 96.7 MB | **179 ms** |
| `omniparser-icon-detect-v2` (Microsoft OmniParser) | `icon` (single class) | 76.7 MB | **183 ms** |

The multi-class model is preferred because it names what it found. OmniParser's
detector only localises icons - it answers "there is a clickable icon here", not
"this is a save button" - so it is only useful paired with OCR.

Install them with:

```powershell
powershell -ExecutionPolicy Bypass -File tools\computer-broker\fetch-vision-models.ps1
```

That downloads the PyTorch checkpoints from `hf-mirror.com` and converts them to
ONNX once, in a Python environment that has torch + ultralytics. **The runtime
only needs onnxruntime** - torch is a build-time dependency for the conversion,
never a runtime one.

### Measured behaviour

On a real 3840x2160 desktop screenshot at `conf=0.4`:

```
/detect    52 detections, detect_ms 184, total 308
/elements 171 elements = 120 OCR + 51 YOLO, total 10.0 s (cold OCR)
```

An overlay of the boxes lands on real controls: browser back/forward/refresh
icons, the tab strip, bookmark bar entries, the search box's microphone and
camera buttons, and every row of an application sidebar. Worth noting because
`ui-elements-detection` was trained on **macOS** screenshots - it transfers to
Windows controls well enough to be useful.

### Honest limitations

* The models see a 640x640 letterboxed copy of the frame, so controls smaller
  than roughly `screen_width / 640 * 8` pixels are missed. On a 4K screen that is
  the difference between "every toolbar button" and "every 48px+ button".
* `AX*` class names are macOS accessibility vocabulary. The boxes are right; do
  not read the label as a Windows control type.
* Neither model reads text. Text still comes from OCR - YOLO is for the icons,
  toggles and canvases that have no text at all.
* Detection and OCR are independent: `/elements` merges them and drops boxes
  that mostly sit on recognised text, but a button and its label can still both
  appear.

## image_vqa now runs local first

`image_vqa` used to send every question to a remote multimodal model, which meant
base64-encoding and uploading the image even for "what does this button say".
It now has three modes (`tools.imageVQA.mode` in config, or a per-call `mode`):

| Mode | Behaviour |
| --- | --- |
| `auto` (default) | answer on device when possible, otherwise call the model |
| `local` | never call the API; returns `local_answer_unavailable` when OCR cannot help |
| `remote` | always call the API (previous behaviour, kept for comparison) |

The local answerer is intentionally narrow. It answers when:

* the question asks for the text itself ("图里写了什么", "extract text"), or
* a recognised label appears literally in the question ("保存按钮在哪",
  "帮我点开始游戏") - the longest such label wins.

It refuses everything else. A question about meaning, mood, appearance or scene
falls through to the model, and a question that merely *resembles* a label is not
treated as a hit, because a confident wrong answer is worse than a fallback.

The response says which path ran (`engine: local_ocr` / `local_ocr+yolo` /
`remote_vlm`) plus `elements_seen` and any `matches`, so cost is visible instead
of implied. A locally answered question never base64-encodes the image either -
that work happens only on the remote path.

Measured: list-intent over a full 4K screenshot returned 147 elements from
`local_ocr`; "文件在哪" resolved to a box; a mood question declined and fell
through. See `TestVisionLocalAnswerSmoke` (set `RUN_VISION_LOCAL_SMOKE=1` and
`VISION_SMOKE_IMAGE`).

## INT8 quantization: measured, and rejected

`tools/computer-broker/quantize-vision-model.py` runs static INT8 quantization and
then measures what it cost, instead of assuming it is fine. Result on the UI
element detector (YOLO11-L, 5 classes, 640 input):

| Configuration | Speedup | Detection retention |
| --- | --- | --- |
| Quantize everything | 1.7x | **0%** - the classification head collapses |
| Exclude the detection head, weak calibration set | 1.3x | 69.5% |
| Exclude the detection head, 195 diverse UI frames | 1.3x | **77.4%** |

Quantizing the whole graph looks fine - it loads, it runs, it is faster - but every
class score comes out exactly zero while the box regression branch survives. The
export bakes in 160 Sigmoid nodes, so flattening the class logits before them
zeroes every detection. Keeping `/model.23/` in FP32 fixes that and still loses
23% of detections.

Trading a quarter of the detections for 1.3x is not worth it, so the quantized
model was deleted and the service keeps using FP32. The script stays so the
measurement can be repeated if a future model or a GPU execution provider changes
the calculus.

One more trap worth recording: the base model must be exported at **opset 13 or
later**. At opset 12 the quantizer emits `DequantizeLinear` with an `axis`
attribute that the runtime rejects, and the failure only appears when the
quantized model is loaded - after quantization "succeeds".
