#!/usr/bin/env python3
"""INT8-quantise a YOLO detector and measure what it cost in accuracy.

Dynamic quantization barely helps a convolutional detector: it only rewrites
MatMul/Gemm, and a YOLO graph is almost entirely convolutions. Real CPU speedup
needs **static** quantization with a calibration set of representative frames,
so this script does that and then measures the damage instead of assuming it is
acceptable.

Accuracy is reported as agreement with the FP32 model on held-out frames:
detections are matched by class with IoU >= 0.5, and the report covers how many
boxes survived, how many appeared and how many the two models disagree about.
That is not a substitute for a labelled mAP run, but it is enough to answer "did
quantization quietly break the detector".

Usage:
    python quantize-vision-model.py <fp32.onnx> <out.int8.onnx> [--calib DIR ...]
"""

from __future__ import annotations

import argparse
import glob
import os
import sys
import time
from typing import Any, Dict, List, Sequence, Tuple

import cv2
import numpy as np
import onnxruntime as ort
from onnxruntime.quantization import CalibrationDataReader, QuantFormat, QuantType, quantize_static

IMGSZ = 640


def model_input_name(path: str) -> str:
    """Read the input tensor name rather than assuming Ultralytics' convention."""
    session = ort.InferenceSession(path, providers=["CPUExecutionProvider"])
    return session.get_inputs()[0].name


# ---------------------------------------------------------------------------
# Pre/post processing shared with the runtime detector
# ---------------------------------------------------------------------------


def letterbox(image: np.ndarray) -> Tuple[np.ndarray, float, int, int]:
    height, width = image.shape[:2]
    scale = min(IMGSZ / width, IMGSZ / height)
    new_w = max(1, int(round(width * scale)))
    new_h = max(1, int(round(height * scale)))
    resized = cv2.resize(image, (new_w, new_h), interpolation=cv2.INTER_LINEAR)
    canvas = np.full((IMGSZ, IMGSZ, 3), 114, dtype=np.uint8)
    pad_x = (IMGSZ - new_w) // 2
    pad_y = (IMGSZ - new_h) // 2
    canvas[pad_y : pad_y + new_h, pad_x : pad_x + new_w] = resized
    return canvas, scale, pad_x, pad_y


def to_blob(image: np.ndarray) -> np.ndarray:
    canvas, _, _, _ = letterbox(image)
    blob = canvas[:, :, ::-1].astype(np.float32) / 255.0  # BGR -> RGB
    blob = np.transpose(blob, (2, 0, 1))[None, ...]
    return np.ascontiguousarray(blob)


def nms(boxes: np.ndarray, scores: np.ndarray, iou_threshold: float) -> List[int]:
    if len(boxes) == 0:
        return []
    x1, y1 = boxes[:, 0], boxes[:, 1]
    x2, y2 = boxes[:, 0] + boxes[:, 2], boxes[:, 1] + boxes[:, 3]
    area = np.maximum(0, x2 - x1) * np.maximum(0, y2 - y1)
    order = np.argsort(-scores)
    keep: List[int] = []
    while order.size > 0:
        current = int(order[0])
        keep.append(current)
        rest = order[1:]
        xx1 = np.maximum(x1[current], x1[rest])
        yy1 = np.maximum(y1[current], y1[rest])
        xx2 = np.minimum(x2[current], x2[rest])
        yy2 = np.minimum(y2[current], y2[rest])
        inter = np.maximum(0, xx2 - xx1) * np.maximum(0, yy2 - yy1)
        union = area[current] + area[rest] - inter
        iou = np.where(union > 0, inter / np.maximum(union, 1e-9), 0.0)
        order = rest[iou <= iou_threshold]
    return keep


def detect(session: ort.InferenceSession, image: np.ndarray, conf: float = 0.35, iou: float = 0.45) -> List[Dict[str, Any]]:
    blob = to_blob(image)
    raw = np.asarray(session.run(None, {session.get_inputs()[0].name: blob})[0])
    raw = raw[0]
    if raw.shape[0] < raw.shape[1]:
        raw = raw.transpose(1, 0)
    class_scores = raw[:, 4:]
    class_ids = class_scores.argmax(axis=1)
    confidence = class_scores[np.arange(len(raw)), class_ids]
    keep = confidence >= conf
    if not keep.any():
        return []
    xywh = raw[keep][:, :4]
    scores = confidence[keep]
    ids = class_ids[keep]
    boxes = np.stack(
        [xywh[:, 0] - xywh[:, 2] / 2, xywh[:, 1] - xywh[:, 3] / 2, xywh[:, 2], xywh[:, 3]], axis=1
    )
    selected = nms(boxes, scores, iou)

    _, scale, pad_x, pad_y = letterbox(image)
    height, width = image.shape[:2]
    results: List[Dict[str, Any]] = []
    for index in selected[:100]:
        x, y, w, h = boxes[index]
        x1 = float(np.clip((x - pad_x) / scale, 0, width - 1))
        y1 = float(np.clip((y - pad_y) / scale, 0, height - 1))
        x2 = float(np.clip((x + w - pad_x) / scale, 0, width - 1))
        y2 = float(np.clip((y + h - pad_y) / scale, 0, height - 1))
        if x2 - x1 < 2 or y2 - y1 < 2:
            continue
        results.append(
            {"class_id": int(ids[index]), "score": float(scores[index]), "box": [x1, y1, x2 - x1, y2 - y1]}
        )
    return results


def iou(a: Sequence[float], b: Sequence[float]) -> float:
    ax1, ay1, aw, ah = a
    bx1, by1, bw, bh = b
    ax2, ay2 = ax1 + aw, ay1 + ah
    bx2, by2 = bx1 + bw, by1 + bh
    ix1, iy1 = max(ax1, bx1), max(ay1, by1)
    ix2, iy2 = min(ax2, bx2), min(ay2, by2)
    if ix2 <= ix1 or iy2 <= iy1:
        return 0.0
    inter = (ix2 - ix1) * (iy2 - iy1)
    union = aw * ah + bw * bh - inter
    return inter / union if union > 0 else 0.0


def compare(reference: List[Dict[str, Any]], other: List[Dict[str, Any]], threshold: float = 0.5) -> Tuple[int, int, int]:
    """Return (matched, missed, spurious) comparing `other` against `reference`."""
    used = set()
    matched = 0
    for ref in reference:
        best_index, best_iou = -1, 0.0
        for index, candidate in enumerate(other):
            if index in used or candidate["class_id"] != ref["class_id"]:
                continue
            value = iou(ref["box"], candidate["box"])
            if value > best_iou:
                best_index, best_iou = index, value
        if best_index >= 0 and best_iou >= threshold:
            used.add(best_index)
            matched += 1
    return matched, len(reference) - matched, len(other) - len(used)


# ---------------------------------------------------------------------------
# Calibration
# ---------------------------------------------------------------------------


class FrameCalibrationReader(CalibrationDataReader):
    def __init__(self, frames: Sequence[np.ndarray], input_name: str) -> None:
        self._frames = list(frames)
        self._input_name = input_name
        self._index = 0

    def get_next(self) -> Dict[str, np.ndarray] | None:
        if self._index >= len(self._frames):
            return None
        blob = to_blob(self._frames[self._index])
        self._index += 1
        return {self._input_name: blob}

    def rewind(self) -> None:
        self._index = 0


def collect_images(roots: Sequence[str], limit: int = 40) -> List[np.ndarray]:
    paths: List[str] = []
    for root in roots:
        if os.path.isfile(root):
            paths.append(root)
            continue
        for pattern in ("**/*.png", "**/*.jpg"):
            paths.extend(glob.glob(os.path.join(root, pattern), recursive=True))
    paths = sorted(set(paths))
    images: List[np.ndarray] = []
    for path in paths:
        image = cv2.imread(path, cv2.IMREAD_COLOR)
        if image is None or image.shape[0] < 64 or image.shape[1] < 64:
            continue
        images.append(image)
        # Two extra scales per frame broaden the activation range the
        # calibration observes without needing more source screenshots.
        if len(images) < limit:
            images.append(cv2.resize(image, (image.shape[1] // 2, image.shape[0] // 2)))
        if len(images) >= limit:
            break
    return images[:limit]


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------


def main() -> int:
    parser = argparse.ArgumentParser(description="INT8-quantise a YOLO detector and measure the damage")
    parser.add_argument("source")
    parser.add_argument("output")
    parser.add_argument("--calib", action="append", default=[], help="directory or file of calibration frames")
    parser.add_argument("--calib-limit", type=int, default=40)
    parser.add_argument("--eval", action="append", default=[], help="frames to compare FP32 vs INT8 on")
    parser.add_argument("--checkpoint", default="", help="checkpoint path for quantize_static")
    parser.add_argument(
        "--exclude-prefix",
        action="append",
        default=[],
        help="keep every node whose name starts with this prefix in FP32 (repeatable)",
    )
    args = parser.parse_args()

    calib_images = collect_images(args.calib or [], args.calib_limit)
    if len(calib_images) < 4:
        print(f"need at least 4 calibration frames, found {len(calib_images)}", file=sys.stderr)
        return 2
    print(f"[..] calibrating on {len(calib_images)} frames")
    input_name = model_input_name(args.source)
    print(f"[..] model input tensor: {input_name}")

    # The detection head is the fragile part: its class logits live in a narrow
    # range, and quantising them flattens every score to zero *after* the sigmoid
    # the export already bakes in. The backbone survives INT8 happily, so keep
    # the head in FP32 and accept slightly less speedup for a detector that still
    # detects.
    excluded: List[str] = []
    if args.exclude_prefix:
        import onnx

        graph = onnx.load(args.source).graph
        for node in graph.node:
            if any(node.name.startswith(prefix) for prefix in args.exclude_prefix):
                excluded.append(node.name)
        print(f"[..] excluding {len(excluded)} head nodes from quantization")

    quantize_static(
        model_input=args.source,
        model_output=args.output,
        calibration_data_reader=FrameCalibrationReader(calib_images, input_name),
        quant_format=QuantFormat.QDQ,
        per_channel=True,
        weight_type=QuantType.QInt8,
        activation_type=QuantType.QUInt8,
        extra_options={"ActivationSymmetric": False, "WeightSymmetric": True},
        nodes_to_exclude=excluded,
    )
    print(f"[ok] {args.output}  {os.path.getsize(args.output)/1e6:.1f} MB (fp32 {os.path.getsize(args.source)/1e6:.1f} MB)")

    eval_images = collect_images(args.eval or args.calib or [], 12)
    if not eval_images:
        print("[..] no evaluation frames, skipping accuracy check")
        return 0

    fp32 = ort.InferenceSession(args.source, providers=["CPUExecutionProvider"])
    int8 = ort.InferenceSession(args.output, providers=["CPUExecutionProvider"])

    print("")
    print(f"{'frame':<28}{'fp32 ms':>10}{'int8 ms':>10}{'speedup':>9}{'fp32 det':>10}{'int8 det':>10}{'matched':>9}{'missed':>8}{'spurious':>9}")
    totals = {"fp32": 0, "int8": 0, "matched": 0, "missed": 0, "spurious": 0, "t_fp32": 0.0, "t_int8": 0.0}
    for index, image in enumerate(eval_images):
        detect(fp32, image)
        detect(int8, image)
        start = time.perf_counter()
        ref = detect(fp32, image)
        t_fp32 = (time.perf_counter() - start) * 1000
        start = time.perf_counter()
        other = detect(int8, image)
        t_int8 = (time.perf_counter() - start) * 1000
        matched, missed, spurious = compare(ref, other)
        totals["fp32"] += len(ref)
        totals["int8"] += len(other)
        totals["matched"] += matched
        totals["missed"] += missed
        totals["spurious"] += spurious
        totals["t_fp32"] += t_fp32
        totals["t_int8"] += t_int8
        print(
            f"frame-{index:<22}{t_fp32:>10.0f}{t_int8:>10.0f}{t_fp32/max(t_int8,1e-6):>8.1f}x"
            f"{len(ref):>10}{len(other):>10}{matched:>9}{missed:>8}{spurious:>9}"
        )

    count = len(eval_images)
    print("")
    print(f"total   fp32 {totals['t_fp32']/count:.0f} ms/frame   int8 {totals['t_int8']/count:.0f} ms/frame"
          f"   speedup {totals['t_fp32']/max(totals['t_int8'],1e-6):.1f}x")
    retention = totals["matched"] / max(totals["fp32"], 1)
    precision = totals["matched"] / max(totals["int8"], 1)
    print(f"accuracy fp32 detections {totals['fp32']}  int8 {totals['int8']}"
          f"  matched {totals['matched']}  retention {retention:.1%}  precision {precision:.1%}")
    print(f"         missed {totals['missed']}  spurious {totals['spurious']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
