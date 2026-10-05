#!/usr/bin/env python3
"""Fairy vision service: lightweight real-time YOLO detection + PP-OCR text.

This is the "fast path" companion to the Windows computer broker. The broker
answers *where is the control* from UI Automation for free; this service answers
the same question from pixels when there is no accessibility tree at all
(games, Electron/Flutter canvas, remote desktop, video).

Design notes that make it real time:

* **Long lived.** ONNX sessions are built once and reused. Loading RapidOCR and
  a YOLO graph costs roughly a second, which would dominate a per-call process.
* **Desktop Duplication capture.** dxcam (DXGI) is used when available, then
  mss, then PIL. On a 4K desktop that is ~5-15 ms instead of ~60-100 ms.
* **Frame change gate.** `skip_unchanged` hashes the captured frame and returns
  `changed: false` without running any model when nothing moved. In a normal UI
  most frames are identical, so this is the single biggest saving.
* **Region of interest.** Every action accepts a `region`, so a caller that
  already knows which window it cares about never pays for the full desktop.
* **Cheap detector, cheap recogniser.** YOLO runs at a fixed square input
  (default 640) regardless of screen size; OCR runs PP-OCRv4 mobile.

Everything is ONNX Runtime on CPU. No torch, no paddle, no GPU requirement.

Protocol (HTTP/JSON on 127.0.0.1):

    GET  /health
    POST /frame     {region?, save_path?, skip_unchanged?}
    POST /ocr       {image_path?|frame:true, region?, min_score?}
    POST /detect    {image_path?|frame:true, region?, conf?, iou?}
    POST /elements  {image_path?|frame:true, region?, ...}   merged, numbered
    POST /find_text {text, image_path?|frame:true, region?, fuzzy?}
    POST /locate    {text?|class?, ...}                      clickable point

Every response carries a `timing` block so callers can see where the budget
goes instead of guessing.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import sys
import threading
import time
import traceback
from difflib import SequenceMatcher
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Dict, List, Optional, Sequence, Tuple

import numpy as np

try:  # pragma: no cover - import guard only
    import cv2
except Exception as exc:  # pragma: no cover
    print("opencv-python is required: %s" % exc, file=sys.stderr)
    raise


# ---------------------------------------------------------------------------
# Screen capture
# ---------------------------------------------------------------------------


class ScreenCapture:
    """Capture the desktop as BGR numpy arrays, preferring the fastest backend."""

    def __init__(self) -> None:
        self.backend = "pil"
        self._dxcam = None
        self._mss = None
        self._lock = threading.Lock()

        try:
            import dxcam  # type: ignore

            self._dxcam = dxcam.create(output_idx=0, output_color="BGR")
            if self._dxcam is not None:
                self.backend = "dxcam"
                return
        except Exception:
            self._dxcam = None

        try:
            import mss  # type: ignore

            self._mss = mss.mss()
            self.backend = "mss"
            return
        except Exception:
            self._mss = None

        self.backend = "pil"

    def grab(self, region: Optional[Dict[str, int]] = None) -> np.ndarray:
        if self.backend == "dxcam":
            frame = self._grab_dxcam(region)
            if frame is not None:
                return frame
        if self.backend == "mss":
            frame = self._grab_mss(region)
            if frame is not None:
                return frame
        return self._grab_pil(region)

    def _grab_dxcam(self, region: Optional[Dict[str, int]]) -> Optional[np.ndarray]:
        try:
            with self._lock:
                if region:
                    left = int(region["x"])
                    top = int(region["y"])
                    right = left + int(region["width"])
                    bottom = top + int(region["height"])
                    frame = self._dxcam.grab(region=(left, top, right, bottom))
                else:
                    frame = self._dxcam.grab()
            if frame is None:
                # DXGI returns None when nothing changed since the last grab.
                return None
            return np.ascontiguousarray(frame)
        except Exception:
            return None

    def _grab_mss(self, region: Optional[Dict[str, int]]) -> Optional[np.ndarray]:
        try:
            with self._lock:
                if region:
                    box = {
                        "left": int(region["x"]),
                        "top": int(region["y"]),
                        "width": int(region["width"]),
                        "height": int(region["height"]),
                    }
                else:
                    box = self._mss.monitors[0]
                shot = self._mss.grab(box)
            return np.ascontiguousarray(np.asarray(shot)[:, :, :3])
        except Exception:
            return None

    def _grab_pil(self, region: Optional[Dict[str, int]]) -> np.ndarray:
        from PIL import ImageGrab

        if region:
            bbox = (
                int(region["x"]),
                int(region["y"]),
                int(region["x"]) + int(region["width"]),
                int(region["y"]) + int(region["height"]),
            )
        else:
            bbox = None
        image = ImageGrab.grab(bbox=bbox, all_screens=True)
        return np.ascontiguousarray(np.asarray(image)[:, :, ::-1])


class CameraCapture:
    """Webcam frames as BGR arrays.

    This is the same pipeline as the screen path on purpose: once a frame is a
    numpy array, YOLO and OCR do not care whether it came from the desktop or a
    camera, so camera support costs one source switch rather than a second
    vision stack.
    """

    def __init__(self, index: int = 0, width: int = 0, height: int = 0) -> None:
        self.index = int(index)
        self.width = int(width)
        self.height = int(height)
        self._capture = None
        self._lock = threading.Lock()

    def _ensure_open(self):
        if self._capture is not None and self._capture.isOpened():
            return self._capture
        # DSHOW opens substantially faster than the default MSMF backend on
        # Windows; fall back to the default when it is unavailable.
        capture = cv2.VideoCapture(self.index, cv2.CAP_DSHOW)
        if not capture.isOpened():
            capture.release()
            capture = cv2.VideoCapture(self.index)
        if not capture.isOpened():
            capture.release()
            raise RuntimeError("cannot open camera index %d" % self.index)
        if self.width > 0:
            capture.set(cv2.CAP_PROP_FRAME_WIDTH, self.width)
        if self.height > 0:
            capture.set(cv2.CAP_PROP_FRAME_HEIGHT, self.height)
        capture.set(cv2.CAP_PROP_BUFFERSIZE, 1)
        self._capture = capture
        return capture

    def grab(self, region: Optional[Dict[str, int]] = None) -> np.ndarray:
        import time as _time

        with self._lock:
            capture = self._ensure_open()
            # Drop any stale buffered frame so the result reflects "now".
            for _ in range(2):
                capture.grab()
            ok, frame = capture.read()
        if not ok or frame is None:
            raise RuntimeError("camera read failed for index %d" % self.index)
        if region:
            x = int(region["x"])
            y = int(region["y"])
            frame = frame[y : y + int(region["height"]), x : x + int(region["width"])]
        return np.ascontiguousarray(frame)

    def release(self) -> None:
        with self._lock:
            if self._capture is not None:
                self._capture.release()
                self._capture = None


# ---------------------------------------------------------------------------
# YOLO (ONNX) detector
# ---------------------------------------------------------------------------


class YoloDetector:
    """Minimal YOLO ONNX runner.

    Supports the common export layouts so a model can be swapped without code
    changes:

      * YOLOv8 / v11 : (1, 4+nc, N)   no objectness
      * YOLOv5 / v7  : (1, N, 5+nc)   with objectness
      * YOLOv10      : (1, N, 6)      end-to-end, already NMS-ed

    Class names come from a sibling ``<model>.labels.txt`` (one name per line)
    or ``<model>.meta.json`` (``{"names": [...]}``); with no label file the
    classes are reported as ``class_<index>``.
    """

    def __init__(self, model_path: str, imgsz: int = 640, threads: int = 0) -> None:
        import onnxruntime as ort

        options = ort.SessionOptions()
        options.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
        if threads > 0:
            options.intra_op_num_threads = threads
        options.enable_cpu_mem_arena = True

        self.session = ort.InferenceSession(
            model_path, sess_options=options, providers=["CPUExecutionProvider"]
        )
        self.input_name = self.session.get_inputs()[0].name
        self.imgsz = int(imgsz)
        self.model_path = model_path
        self.names = self._load_names(model_path)
        self.end_to_end = False

    @staticmethod
    def _load_names(model_path: str) -> List[str]:
        base, _ = os.path.splitext(model_path)
        labels = base + ".labels.txt"
        meta = base + ".meta.json"
        if os.path.exists(meta):
            try:
                with open(meta, "r", encoding="utf-8") as handle:
                    payload = json.load(handle)
                names = payload.get("names")
                if isinstance(names, dict):
                    return [str(names[key]) for key in sorted(names, key=lambda k: int(k))]
                if isinstance(names, list):
                    return [str(item) for item in names]
            except Exception:
                pass
        if os.path.exists(labels):
            try:
                with open(labels, "r", encoding="utf-8") as handle:
                    return [line.strip() for line in handle if line.strip()]
            except Exception:
                pass
        return []

    def _letterbox(self, image: np.ndarray) -> Tuple[np.ndarray, float, int, int]:
        height, width = image.shape[:2]
        scale = min(self.imgsz / width, self.imgsz / height)
        new_w = max(1, int(round(width * scale)))
        new_h = max(1, int(round(height * scale)))
        resized = cv2.resize(image, (new_w, new_h), interpolation=cv2.INTER_LINEAR)
        canvas = np.full((self.imgsz, self.imgsz, 3), 114, dtype=np.uint8)
        pad_x = (self.imgsz - new_w) // 2
        pad_y = (self.imgsz - new_h) // 2
        canvas[pad_y : pad_y + new_h, pad_x : pad_x + new_w] = resized
        return canvas, scale, pad_x, pad_y

    def detect(
        self,
        image: np.ndarray,
        conf: float = 0.25,
        iou: float = 0.45,
        max_det: int = 100,
    ) -> List[Dict[str, Any]]:
        canvas, scale, pad_x, pad_y = self._letterbox(image)
        blob = canvas[:, :, ::-1].astype(np.float32) / 255.0
        blob = np.transpose(blob, (2, 0, 1))[None, ...]
        blob = np.ascontiguousarray(blob)

        outputs = self.session.run(None, {self.input_name: blob})
        raw = np.asarray(outputs[0])
        if raw.ndim != 3:
            raise RuntimeError("unexpected YOLO output rank: %d" % raw.ndim)
        raw = raw[0]

        # YOLOv8/v11 export as (channels, anchors); v5/v7/v10 as (anchors, channels).
        if raw.shape[0] < raw.shape[1]:
            raw = raw.transpose(1, 0)

        columns = raw.shape[1]
        if columns == 6 and not self.names:
            # End-to-end export: x1, y1, x2, y2, score, class.
            self.end_to_end = True
        elif columns == 6 and self.names and len(self.names) == 1:
            self.end_to_end = True
        boxes: List[List[float]] = []
        scores: List[float] = []
        class_ids: List[int] = []

        if self.end_to_end:
            for row in raw:
                score = float(row[4])
                if score < conf:
                    continue
                x1, y1, x2, y2 = (float(row[0]), float(row[1]), float(row[2]), float(row[3]))
                boxes.append([x1, y1, x2 - x1, y2 - y1])
                scores.append(score)
                class_ids.append(int(row[5]))
        else:
            nc = columns - 4
            has_objectness = False
            if self.names:
                if nc == len(self.names):
                    has_objectness = False
                elif nc - 1 == len(self.names):
                    has_objectness = True
            elif nc == 1:
                has_objectness = True

            if has_objectness:
                # v5/v7: cx, cy, w, h, obj, cls...
                objectness = raw[:, 4]
                class_scores = raw[:, 5:]
                class_ids_arr = class_scores.argmax(axis=1)
                confidence = objectness * class_scores[np.arange(len(raw)), class_ids_arr]
                xywh = raw[:, :4]
            else:
                # v8/v11: cx, cy, w, h, cls...
                class_scores = raw[:, 4:]
                class_ids_arr = class_scores.argmax(axis=1)
                confidence = class_scores[np.arange(len(raw)), class_ids_arr]
                xywh = raw[:, :4]

            keep = confidence >= conf
            for index in np.nonzero(keep)[0]:
                cx, cy, w, h = xywh[index]
                boxes.append([float(cx - w / 2), float(cy - h / 2), float(w), float(h)])
                scores.append(float(confidence[index]))
                class_ids.append(int(class_ids_arr[index]))

        if not boxes:
            return []

        keep_indices = _nms(boxes, scores, iou, max_det) if not self.end_to_end else list(range(len(boxes)))
        height, width = image.shape[:2]
        results: List[Dict[str, Any]] = []
        for index in keep_indices:
            x, y, w, h = boxes[index]
            # Undo letterbox padding and scaling back to source pixels.
            x1 = (x - pad_x) / scale
            y1 = (y - pad_y) / scale
            x2 = (x + w - pad_x) / scale
            y2 = (y + h - pad_y) / scale
            x1 = float(np.clip(x1, 0, width - 1))
            y1 = float(np.clip(y1, 0, height - 1))
            x2 = float(np.clip(x2, 0, width - 1))
            y2 = float(np.clip(y2, 0, height - 1))
            if x2 - x1 < 2 or y2 - y1 < 2:
                continue
            class_id = class_ids[index]
            label = self.names[class_id] if 0 <= class_id < len(self.names) else "class_%d" % class_id
            results.append(
                {
                    "class_id": class_id,
                    "label": label,
                    "score": round(float(scores[index]), 4),
                    "box": {
                        "x": int(round(x1)),
                        "y": int(round(y1)),
                        "width": int(round(x2 - x1)),
                        "height": int(round(y2 - y1)),
                    },
                }
            )
        results.sort(key=lambda item: (-item["score"], item["box"]["y"], item["box"]["x"]))
        return results


def _nms(boxes: Sequence[Sequence[float]], scores: Sequence[float], iou_threshold: float, max_det: int) -> List[int]:
    if not boxes:
        return []
    array = np.asarray(boxes, dtype=np.float32)
    x1 = array[:, 0]
    y1 = array[:, 1]
    x2 = array[:, 0] + array[:, 2]
    y2 = array[:, 1] + array[:, 3]
    area = np.maximum(0.0, x2 - x1) * np.maximum(0.0, y2 - y1)
    order = np.argsort(-np.asarray(scores, dtype=np.float32))

    keep: List[int] = []
    while order.size > 0 and len(keep) < max_det:
        current = int(order[0])
        keep.append(current)
        if order.size == 1:
            break
        rest = order[1:]
        xx1 = np.maximum(x1[current], x1[rest])
        yy1 = np.maximum(y1[current], y1[rest])
        xx2 = np.minimum(x2[current], x2[rest])
        yy2 = np.minimum(y2[current], y2[rest])
        inter = np.maximum(0.0, xx2 - xx1) * np.maximum(0.0, yy2 - yy1)
        union = area[current] + area[rest] - inter
        iou = np.where(union > 0, inter / np.maximum(union, 1e-9), 0.0)
        order = rest[iou <= iou_threshold]
    return keep


# ---------------------------------------------------------------------------
# OCR
# ---------------------------------------------------------------------------


class OcrEngine:
    def __init__(self, threads: int = 0) -> None:
        from rapidocr_onnxruntime import RapidOCR

        self.engine = RapidOCR()
        # Screenshots are essentially always horizontal text, so the angle
        # classifier is one wasted forward pass per box. Measured saving is
        # ~10% on a 120 box screen; free accuracy trade-off on a desktop.
        self.engine.use_angle_cls = False
        # PP-OCR's default limit_type='min' never shrinks an image whose short
        # side already exceeds the limit, so a 4K screen is detected at full
        # resolution. Capping the long side is the documented way to bound
        # detection cost, and boxes are scaled back to source pixels.
        try:
            self.engine.text_detector.limit_type = "max"
            self.engine.text_detector.limit_side_len = 1600
        except Exception:
            pass

    def read(self, image: np.ndarray, min_score: float = 0.5) -> List[Dict[str, Any]]:
        result, _ = self.engine(image)
        items: List[Dict[str, Any]] = []
        if not result:
            return items
        for entry in result:
            try:
                box, text, score = entry[0], entry[1], float(entry[2])
            except Exception:
                continue
            if score < min_score or not text:
                continue
            xs = [float(point[0]) for point in box]
            ys = [float(point[1]) for point in box]
            x1, x2 = min(xs), max(xs)
            y1, y2 = min(ys), max(ys)
            items.append(
                {
                    "text": str(text),
                    "score": round(score, 4),
                    "box": {
                        "x": int(round(x1)),
                        "y": int(round(y1)),
                        "width": int(round(x2 - x1)),
                        "height": int(round(y2 - y1)),
                    },
                }
            )
        items.sort(key=lambda item: (item["box"]["y"], item["box"]["x"]))
        return items


# ---------------------------------------------------------------------------
# Service
# ---------------------------------------------------------------------------


def _overlap_ratio(inner: Dict[str, int], outer: Dict[str, int]) -> float:
    x1 = max(inner["x"], outer["x"])
    y1 = max(inner["y"], outer["y"])
    x2 = min(inner["x"] + inner["width"], outer["x"] + outer["width"])
    y2 = min(inner["y"] + inner["height"], outer["y"] + outer["height"])
    if x2 <= x1 or y2 <= y1:
        return 0.0
    intersection = (x2 - x1) * (y2 - y1)
    area = max(1, inner["width"] * inner["height"])
    return intersection / area


class VisionService:
    def __init__(self, model_path: Optional[str], imgsz: int, threads: int) -> None:
        self.capture = ScreenCapture()
        self.camera: Optional[CameraCapture] = None
        self.threads = threads
        self.imgsz = imgsz
        self.model_path = model_path
        # NOTE: these are deliberately not called `self.ocr` / `self.detect` -
        # the instances would shadow the identically named request handlers.
        self._detector: Optional[YoloDetector] = None
        self.detector_error: Optional[str] = None
        self._ocr_engine: Optional[OcrEngine] = None
        self.ocr_error: Optional[str] = None
        self.lock = threading.Lock()
        self.last_frame_signature: Optional[bytes] = None
        self.startup = time.time()
        self.last_request_at = time.time()
        self.shutdown_requested = threading.Event()
        self.session = {"active": False, "mode": None, "started_at": None, "region": None}
        # Frame-signature keyed result cache. The expensive part of vision is one
        # pass per *screen state*, not per question: an agent typically asks
        # "where is X", then "where is Y" about the same frame, and a static UI
        # does not change between those questions. Measured: ~1.9 s for a window
        # region, ~0.005 s on a cache hit.
        self._ocr_cache: List[Tuple[bytes, float, List[Dict[str, Any]]]] = []
        self._cache_limit = 8
        self.cache_hits = 0
        self.cache_misses = 0

    @staticmethod
    def _signature_delta(left: Optional[bytes], right: Optional[bytes]) -> float:
        """Fraction of coarse fingerprint cells that differ meaningfully."""
        if left is None or right is None or len(left) != len(right):
            return 1.0
        a = np.frombuffer(left, dtype=np.int16)
        b = np.frombuffer(right, dtype=np.int16)
        return float((np.abs(a - b) > 12).mean())

    def same_frame(self, left: Optional[bytes], right: Optional[bytes]) -> bool:
        return self._signature_delta(left, right) <= 0.02

    def _cache_get(self, signature: bytes, min_score: float) -> Optional[List[Dict[str, Any]]]:
        for cached_signature, cached_score, texts in self._ocr_cache:
            if abs(cached_score - min_score) > 1e-6:
                continue
            if self.same_frame(cached_signature, signature):
                return texts
        return None

    def _cache_put(self, signature: bytes, min_score: float, texts: List[Dict[str, Any]]) -> None:
        if len(self._ocr_cache) >= self._cache_limit:
            self._ocr_cache.pop(0)
        self._ocr_cache.append((signature, min_score, texts))

    # -- session lifecycle --------------------------------------------------

    def start_session(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        """Mark a real-time observation session as active.

        The agent decides when to begin watching the screen or the camera. While
        a session is open the process stays resident so ONNX sessions, the DXGI
        capture buffer and the camera device are all kept warm; when it closes
        (or the idle timeout fires) the caller can shut the process down.
        """
        mode = str(payload.get("mode", "screen")).lower()
        if mode not in ("screen", "camera"):
            raise ValueError("mode must be 'screen' or 'camera'")
        self.session = {
            "active": True,
            "mode": mode,
            "started_at": time.time(),
            "region": payload.get("region"),
        }
        if mode == "camera":
            self.camera = CameraCapture(
                int(payload.get("camera_index", 0)),
                int(payload.get("camera_width", 0)),
                int(payload.get("camera_height", 0)),
            )
            self.camera._ensure_open()  # fail fast if the device is unavailable
        # Warm the models now so the first real frame is not the slow one.
        if payload.get("warmup", True):
            self.ensure_ocr()
            self.ensure_detector()
        return {"session": dict(self.session), "health": self.health()}

    def stop_session(self) -> Dict[str, Any]:
        self.session = {"active": False, "mode": None, "started_at": None, "region": None}
        if self.camera is not None:
            self.camera.release()
            self.camera = None
        return {"session": dict(self.session)}

    # -- model lifecycle ---------------------------------------------------

    def ensure_ocr(self) -> OcrEngine:
        if self._ocr_engine is None:
            self._ocr_engine = OcrEngine(self.threads)
            self.ocr_error = None
        return self._ocr_engine

    def ensure_detector(self) -> Optional[YoloDetector]:
        if self._detector is not None:
            return self._detector
        if not self.model_path or not os.path.exists(self.model_path):
            self.detector_error = "no YOLO model at %s" % (self.model_path or "<unset>")
            return None
        try:
            self._detector = YoloDetector(self.model_path, self.imgsz, self.threads)
            self.detector_error = None
        except Exception as exc:
            self.detector_error = "%s: %s" % (type(exc).__name__, exc)
            return None
        return self._detector

    # -- frames ------------------------------------------------------------

    def obtain_image(self, payload: Dict[str, Any], timing: Dict[str, float]) -> np.ndarray:
        path = payload.get("image_path")
        if path:
            start = time.perf_counter()
            image = cv2.imread(str(path), cv2.IMREAD_COLOR)
            timing["read_ms"] = round((time.perf_counter() - start) * 1000, 2)
            if image is None:
                raise FileNotFoundError("cannot read image: %s" % path)
            return image

        mode = str(payload.get("source") or self.session.get("mode") or "screen").lower()
        if mode == "camera":
            start = time.perf_counter()
            if self.camera is None:
                self.camera = CameraCapture(
                    int(payload.get("camera_index", 0)),
                    int(payload.get("camera_width", 0)),
                    int(payload.get("camera_height", 0)),
                )
            image = self.camera.grab(payload.get("region") or self.session.get("region"))
            timing["grab_ms"] = round((time.perf_counter() - start) * 1000, 2)
            timing["capture_backend"] = "camera:%d" % self.camera.index
            return image

        start = time.perf_counter()
        image = self.capture.grab(payload.get("region") or self.session.get("region"))
        timing["grab_ms"] = round((time.perf_counter() - start) * 1000, 2)
        timing["capture_backend"] = self.capture.backend
        return image

    def frame_signature(self, image: np.ndarray) -> bytes:
        """Coarse fingerprint of a frame, used for the change gate and the cache.

        An exact hash looks correct and behaves badly: a blinking text caret, a
        clock in the taskbar or any 1px animation changes a handful of pixels, so
        every lookup misses and the cache never pays for itself. Downscaling to
        32x18 greyscale and comparing with a small tolerance keeps real UI changes
        detectable while ignoring caret and antialiasing noise.
        """
        small = cv2.resize(image, (32, 18), interpolation=cv2.INTER_AREA)
        if small.ndim == 3:
            small = cv2.cvtColor(small, cv2.COLOR_BGR2GRAY)
        return np.ascontiguousarray(small.astype(np.int16)).tobytes()

    # -- actions -----------------------------------------------------------

    def health(self) -> Dict[str, Any]:
        return {
            "ok": True,
            "uptime_s": round(time.time() - self.startup, 2),
            "capture_backend": self.capture.backend,
            "camera_open": self.camera is not None,
            "session": dict(self.session),
            "last_request_age_s": round(time.time() - self.last_request_at, 2),
            "yolo_model": self.model_path,
            "yolo_loaded": self._detector is not None,
            "yolo_error": self.detector_error,
            "ocr_loaded": self._ocr_engine is not None,
            "ocr_error": self.ocr_error,
            "imgsz": self.imgsz,
            "threads": self.threads,
            "cache": {
                "entries": len(self._ocr_cache),
                "hits": self.cache_hits,
                "misses": self.cache_misses,
            },
        }

    def frame(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        timing: Dict[str, Any] = {}
        with self.lock:
            image = self.obtain_image(payload, timing)
            signature = self.frame_signature(image)
            changed = not self.same_frame(self.last_frame_signature, signature)
            self.last_frame_signature = signature
            save_path = payload.get("save_path")
            if save_path:
                directory = os.path.dirname(os.path.abspath(save_path))
                if directory:
                    os.makedirs(directory, exist_ok=True)
                cv2.imwrite(save_path, image)
        return {
            "changed": changed,
            "signature": hashlib.blake2b(signature, digest_size=8).hexdigest(),
            "width": int(image.shape[1]),
            "height": int(image.shape[0]),
            "save_path": save_path,
            "timing": timing,
        }

    def ocr(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        timing: Dict[str, Any] = {}
        with self.lock:
            image = self.obtain_image(payload, timing)
            signature = self.frame_signature(image)
            min_score = float(payload.get("min_score", 0.5))
            unchanged = self.same_frame(self.last_frame_signature, signature)
            if payload.get("skip_unchanged") and not payload.get("image_path") and unchanged:
                cached = self._cache_get(signature, min_score)
                if cached is not None:
                    timing["cache"] = "hit"
                    self.cache_hits += 1
                    return {
                        "texts": cached,
                        "count": len(cached),
                        "changed": False,
                        "cached": True,
                        "signature": hashlib.blake2b(signature, digest_size=8).hexdigest(),
                        "timing": timing,
                    }
                return {
                    "texts": [],
                    "count": 0,
                    "changed": False,
                    "signature": hashlib.blake2b(signature, digest_size=8).hexdigest(),
                    "timing": timing,
                }

            if not payload.get("no_cache", False):
                cached = self._cache_get(signature, min_score)
                if cached is not None:
                    self.cache_hits += 1
                    timing["cache"] = "hit"
                    self.last_frame_signature = signature
                    return {
                        "texts": cached,
                        "count": len(cached),
                        "changed": not unchanged,
                        "cached": True,
                        "signature": hashlib.blake2b(signature, digest_size=8).hexdigest(),
                        "timing": timing,
                    }

            self.last_frame_signature = signature
            start = time.perf_counter()
            engine = self.ensure_ocr()
            texts = engine.read(image, min_score)
            timing["ocr_ms"] = round((time.perf_counter() - start) * 1000, 2)
            self.cache_misses += 1
            if not payload.get("no_cache", False):
                self._cache_put(signature, min_score, texts)
        return {
            "texts": texts,
            "count": len(texts),
            "changed": not unchanged,
            "cached": False,
            "signature": hashlib.blake2b(signature, digest_size=8).hexdigest(),
            "timing": timing,
        }

    def detect(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        timing: Dict[str, Any] = {}
        with self.lock:
            image = self.obtain_image(payload, timing)
            detector = self.ensure_detector()
            if detector is None:
                return {"detections": [], "count": 0, "error": self.detector_error, "timing": timing}
            start = time.perf_counter()
            detections = detector.detect(
                image,
                conf=float(payload.get("conf", 0.25)),
                iou=float(payload.get("iou", 0.45)),
                max_det=int(payload.get("max_det", 100)),
            )
            timing["detect_ms"] = round((time.perf_counter() - start) * 1000, 2)
        return {"detections": detections, "count": len(detections), "timing": timing}

    def elements(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        timing: Dict[str, Any] = {}
        with self.lock:
            image = self.obtain_image(payload, timing)
            elements: List[Dict[str, Any]] = []

            if payload.get("ocr", True):
                start = time.perf_counter()
                engine = self.ensure_ocr()
                texts = engine.read(image, float(payload.get("min_score", 0.5)))
                timing["ocr_ms"] = round((time.perf_counter() - start) * 1000, 2)
            else:
                texts = []

            detections: List[Dict[str, Any]] = []
            if payload.get("yolo", True):
                detector = self.ensure_detector()
                if detector is not None:
                    start = time.perf_counter()
                    detections = detector.detect(
                        image,
                        conf=float(payload.get("conf", 0.25)),
                        iou=float(payload.get("iou", 0.45)),
                    )
                    timing["detect_ms"] = round((time.perf_counter() - start) * 1000, 2)
                else:
                    timing["detect_ms"] = 0.0

            for item in texts:
                elements.append(
                    {"source": "ocr", "name": item["text"], "bounds": item["box"], "confidence": item["score"]}
                )
            for item in detections:
                box = item["box"]
                # A text label already describes the control better than "icon",
                # so drop detections that mostly sit on recognised text.
                if any(_overlap_ratio(box, other["box"]) > 0.6 for other in texts):
                    continue
                elements.append(
                    {
                        "source": "yolo",
                        "name": item["label"],
                        "bounds": box,
                        "confidence": item["score"],
                    }
                )

            elements.sort(key=lambda entry: (entry["bounds"]["y"], entry["bounds"]["x"]))
            for index, entry in enumerate(elements, start=1):
                entry["id"] = index
                bounds = entry["bounds"]
                entry["center"] = {
                    "x": bounds["x"] + bounds["width"] // 2,
                    "y": bounds["y"] + bounds["height"] // 2,
                }

        return {"elements": elements, "count": len(elements), "timing": timing}

    def find_text(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        needle = str(payload.get("text", "")).strip()
        if not needle:
            raise ValueError("text is required")
        # Fuzzy matching is useful ("开始游戏" vs "开始游 戏") but a weak best match
        # must not be reported as a hit: a confident wrong answer makes the agent
        # click the wrong control, which is strictly worse than "not found".
        min_ratio = float(payload.get("min_ratio", 0.6))
        result = self.ocr(payload)
        texts = result.get("texts", [])
        if not texts:
            return {"matched": None, "candidates": [], "timing": result.get("timing", {})}

        normalized = needle.casefold()
        scored: List[Tuple[float, Dict[str, Any]]] = []
        for item in texts:
            candidate = item["text"].casefold()
            if candidate == normalized:
                ratio = 1.0
            elif normalized in candidate:
                ratio = 0.97
            elif candidate in normalized:
                ratio = 0.9
            else:
                ratio = SequenceMatcher(None, normalized, candidate).ratio()
            scored.append((ratio, item))
        scored.sort(key=lambda pair: -pair[0])
        best_ratio, best = scored[0]
        candidates = [
            {"text": item["text"], "ratio": round(ratio, 4), "box": item["box"]}
            for ratio, item in scored[:5]
        ]
        if best_ratio < min_ratio:
            return {
                "matched": None,
                "candidates": candidates,
                "reason": "no_confident_match",
                "best_ratio": round(best_ratio, 4),
                "min_ratio": min_ratio,
                "timing": result.get("timing", {}),
            }
        box = best["box"]
        return {
            "matched": {**best, "ratio": round(best_ratio, 4), "center": {"x": box["x"] + box["width"] // 2, "y": box["y"] + box["height"] // 2}},
            "candidates": candidates,
            "timing": result.get("timing", {}),
        }

    def locate(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        """Answer "where do I click" from pixels alone."""
        if payload.get("text"):
            found = self.find_text(payload)
            matched = found.get("matched")
            if not matched:
                return {
                    "found": False,
                    "reason": found.get("reason") or "text_not_found",
                    "candidates": found.get("candidates", []),
                    "timing": found.get("timing", {}),
                }
            return {
                "found": True,
                "source": "ocr",
                "name": matched["text"],
                "confidence": matched["ratio"],
                "bounds": matched["box"],
                "point": matched["center"],
                "timing": found.get("timing", {}),
            }

        wanted = str(payload.get("class", "")).strip().casefold()
        result = self.detect(payload)
        detections = result.get("detections", [])
        if wanted:
            detections = [item for item in detections if wanted in item["label"].casefold()]
        if not detections:
            return {
                "found": False,
                "reason": result.get("error") or "no_detection",
                "timing": result.get("timing", {}),
            }
        best = detections[0]
        box = best["box"]
        return {
            "found": True,
            "source": "yolo",
            "name": best["label"],
            "confidence": best["score"],
            "bounds": box,
            "point": {"x": box["x"] + box["width"] // 2, "y": box["y"] + box["height"] // 2},
            "timing": result.get("timing", {}),
        }


# ---------------------------------------------------------------------------
# HTTP plumbing
# ---------------------------------------------------------------------------


def _make_handler(service: VisionService):
    """Build the request handler bound to one service instance."""
    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, fmt: str, *args: Any) -> None:  # keep stdout clean
            pass

        def _send(self, status: int, payload: Dict[str, Any]) -> None:
            body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def _read_payload(self) -> Dict[str, Any]:
            length = int(self.headers.get("Content-Length") or 0)
            if length <= 0:
                return {}
            raw = self.rfile.read(length)
            return json.loads(raw.decode("utf-8"))

        def do_GET(self) -> None:  # noqa: N802
            if self.path.rstrip("/") in ("/health", ""):
                self._send(200, service.health())
            else:
                self._send(404, {"ok": False, "error": "unknown path"})

        def do_POST(self) -> None:  # noqa: N802
            route = self.path.rstrip("/").lstrip("/")
            service.last_request_at = time.time()

            if route == "shutdown":
                self._send(200, {"ok": True, "shutting_down": True})
                service.shutdown_requested.set()
                return

            handlers = {
                "frame": service.frame,
                "ocr": service.ocr,
                "detect": service.detect,
                "elements": service.elements,
                "find_text": service.find_text,
                "locate": service.locate,
                "session/start": service.start_session,
                "session/stop": lambda _payload: service.stop_session(),
            }
            handler = handlers.get(route)
            if handler is None:
                self._send(404, {"ok": False, "error": "unknown action %s" % route})
                return
            try:
                payload = self._read_payload()
                start = time.perf_counter()
                result = handler(payload)
                result = apply_region_offset(payload, result)
                result.setdefault("ok", True)
                timing = result.setdefault("timing", {})
                timing["total_ms"] = round((time.perf_counter() - start) * 1000, 2)
                self._send(200, result)
            except Exception as exc:
                self._send(
                    500,
                    {
                        "ok": False,
                        "error": "%s: %s" % (type(exc).__name__, exc),
                        "traceback": traceback.format_exc().splitlines()[-6:],
                    },
                )

    return Handler


def _parent_alive(pid: int) -> bool:
    """True while the process that started this service is still running.

    The agent owns this process: if the agent exits (cleanly or not) the service
    must not linger, so it polls the parent handle rather than trusting a goodbye
    message.
    """
    if pid <= 0:
        return True
    if os.name == "nt":
        import ctypes

        SYNCHRONIZE = 0x00100000
        WAIT_TIMEOUT = 0x00000102
        kernel32 = ctypes.windll.kernel32
        handle = kernel32.OpenProcess(SYNCHRONIZE, False, pid)
        if not handle:
            return False
        try:
            return kernel32.WaitForSingleObject(handle, 0) == WAIT_TIMEOUT
        finally:
            kernel32.CloseHandle(handle)
    try:
        os.kill(pid, 0)
        return True
    except OSError:
        return False


def _shift_box(box: Optional[Dict[str, Any]], dx: int, dy: int) -> None:
    if isinstance(box, dict):
        box["x"] = int(box.get("x", 0)) + dx
        box["y"] = int(box.get("y", 0)) + dy


def _shift_entry(entry: Dict[str, Any], dx: int, dy: int) -> None:
    _shift_box(entry.get("box"), dx, dy)
    _shift_box(entry.get("bounds"), dx, dy)
    center = entry.get("center")
    if isinstance(center, dict):
        center["x"] = int(center.get("x", 0)) + dx
        center["y"] = int(center.get("y", 0)) + dy


def apply_region_offset(payload: Dict[str, Any], result: Dict[str, Any]) -> Dict[str, Any]:
    """Translate a region-relative answer into screen pixels.

    The caller says "look at this rectangle of the desktop"; everything the
    service finds is naturally relative to that crop. Translating here means
    every client - the agent, a smoke script, a plain curl - receives coordinates
    it can hand straight to the pointer tool, instead of each caller having to
    remember to add the origin.
    """
    region = payload.get("region")
    if not isinstance(region, dict) or str(payload.get("source", "screen")).lower() == "camera":
        return result
    if payload.get("image_path"):
        # A still image on disk has no relationship to the live desktop.
        return result
    dx = int(region.get("x", 0))
    dy = int(region.get("y", 0))
    if dx == 0 and dy == 0:
        result.setdefault("coordinate_space", "screen")
        return result

    for key in ("texts", "elements", "detections"):
        items = result.get(key)
        if isinstance(items, list):
            for item in items:
                if isinstance(item, dict):
                    _shift_entry(item, dx, dy)
    for key in ("matched",):
        entry = result.get(key)
        if isinstance(entry, dict):
            _shift_entry(entry, dx, dy)
    _shift_box(result.get("bounds"), dx, dy)
    point = result.get("point")
    if isinstance(point, dict):
        point["x"] = int(point.get("x", 0)) + dx
        point["y"] = int(point.get("y", 0)) + dy
    candidates = result.get("candidates")
    if isinstance(candidates, list):
        for item in candidates:
            if isinstance(item, dict):
                _shift_box(item.get("box"), dx, dy)
    result["region_origin"] = {"x": dx, "y": dy}
    result["coordinate_space"] = "screen"
    return result


def _watchdog(service: VisionService, server: ThreadingHTTPServer, idle_timeout: float, parent_pid: int) -> None:
    """Shut the service down when it is no longer needed.

    Two rules, matching how the agent uses it:

    * While a screen/camera session is active the process stays resident no
      matter how quiet it is - that is the "real-time observation" window.
    * Once no session is active, an idle timeout reclaims the memory and the
      camera device without waiting for the agent to remember to stop it.
    """
    while not service.shutdown_requested.is_set():
        time.sleep(0.5)
        if parent_pid and not _parent_alive(parent_pid):
            print("[vision] parent process exited, shutting down", flush=True)
            break
        if service.session.get("active"):
            continue
        if idle_timeout > 0 and (time.time() - service.last_request_at) > idle_timeout:
            print("[vision] idle for %.0fs, shutting down" % idle_timeout, flush=True)
            break
    service.shutdown_requested.set()
    try:
        server.shutdown()
    except Exception:
        pass


def main() -> int:
    parser = argparse.ArgumentParser(description="Fairy vision service")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8791)
    parser.add_argument("--model", default=os.environ.get("FAIRY_VISION_YOLO", ""))
    parser.add_argument("--model-dir", default=os.environ.get("FAIRY_VISION_MODEL_DIR", ""))
    parser.add_argument("--imgsz", type=int, default=int(os.environ.get("FAIRY_VISION_IMGSZ", "640")))
    parser.add_argument("--threads", type=int, default=int(os.environ.get("FAIRY_VISION_THREADS", "0")))
    parser.add_argument("--warmup", action="store_true", help="load both models before serving")
    parser.add_argument(
        "--idle-timeout",
        type=float,
        default=float(os.environ.get("FAIRY_VISION_IDLE_TIMEOUT", "0")),
        help="seconds without a request to self-terminate when no session is active (0 = never)",
    )
    parser.add_argument(
        "--parent-pid",
        type=int,
        default=int(os.environ.get("FAIRY_VISION_PARENT_PID", "0")),
        help="exit when this process disappears, so the service never outlives the agent",
    )
    args = parser.parse_args()

    model_path = args.model
    if not model_path and args.model_dir:
        # Preference order matters: the multi-class UI model names what it found
        # (button / link / text area), while the OmniParser icon detector only
        # says "icon". Both are YOLO; pick the one with usable semantics first.
        for candidate in (
            "ui-elements-detection.onnx",
            "yolo-ui.onnx",
            "yolo.onnx",
            "omniparser-icon-detect-v2.onnx",
            "model.onnx",
        ):
            full = os.path.join(args.model_dir, candidate)
            if os.path.exists(full):
                model_path = full
                break

    service = VisionService(model_path or None, args.imgsz, args.threads)
    if args.warmup:
        try:
            service.ensure_ocr()
        except Exception as exc:
            print("[vision] OCR warmup failed: %s" % exc, file=sys.stderr, flush=True)
        service.ensure_detector()

    server = ThreadingHTTPServer((args.host, args.port), _make_handler(service))
    watchdog = threading.Thread(
        target=_watchdog,
        args=(service, server, args.idle_timeout, args.parent_pid),
        name="vision-watchdog",
        daemon=True,
    )
    watchdog.start()
    print(
        "[vision] listening on http://%s:%d capture=%s model=%s idle=%ss parent=%s"
        % (
            args.host,
            args.port,
            service.capture.backend,
            model_path or "<none>",
            args.idle_timeout,
            args.parent_pid or "-",
        ),
        flush=True,
    )
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        service.stop_session()
        server.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
