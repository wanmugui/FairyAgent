#!/usr/bin/env python3
"""Tidy up workspace/result so the delivery area stops being one giant pile.

Layout follows the Windows "known folders" idea: a few top-level folders
named after what you DO with a file, not after its format.

    workspace/result/
      Documents/    reports, spreadsheets, slides  (daily reports by date)
      Pictures/     images, grouped by subject prefix
      Media/        video + audio
      Code/         source files
      Data/         json / yaml / xml
      Models3D/     blend / glb / fbx
      Archives/     zip / tar
      INDEX.md      what lives where

Idempotent: re-running it is safe. Nothing is deleted. Existing
sub-folders that are already projects (mv-comet-alchemist/, 3d-demo/,
bxue-review/ ...) are left untouched -- only loose files at the root
are moved.

Usage:
    python3 tools/organize_result.py             # archive loose files
    python3 tools/organize_result.py --dry-run   # show what would move
    python3 tools/organize_result.py --index-only
"""

import argparse
import re
import shutil
import sys
from datetime import datetime
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
RESULT = REPO / "workspace" / "result"
sys.path.insert(0, str(REPO / "tools"))

from fm_type_catalog import CATEGORIES, FOLDER_ORDER, category_of, folder_of  # noqa: E402

# `2026-10-07-2026-10-07-report.md` / `2026-10-07-report.md` -> 2026-10-07
DATE_PREFIX = re.compile(r"^(\d{4}-\d{2}-\d{2})")
REPORT_SUFFIX = re.compile(r"(report|报告|会话恢复)", re.IGNORECASE)

# Image subject prefixes worth their own sub-folder inside Pictures/.
# Matched case-sensitively against the part before the first "_" or "-".
PICTURE_GROUPS = [
    ("zzz", "zzz"),
    ("fairy", "fairy"),
    ("agent", "agent"),
]


def picture_group(name: str):
    """Sub-folder inside Pictures/, or None for a flat file."""
    stem = name.rsplit(".", 1)[0]
    for prefix, folder in PICTURE_GROUPS:
        if stem.lower().startswith(prefix):
            return folder
    return None


def target_dir_for(path: Path):
    """Where should a loose result file live?"""
    folder = folder_of(path.name)
    if folder is None:
        return None
    base = RESULT / folder
    m = DATE_PREFIX.match(path.name)
    if folder == "Documents" and m and REPORT_SUFFIX.search(path.name):
        # Daily reports: group by their date.
        return base / m.group(1)
    if folder == "Pictures":
        grp = picture_group(path.name)
        if grp:
            return base / grp
    return base


def unique_dest(dest_dir: Path, name: str):
    """Never clobber: pick a numbered variant if the target exists."""
    dest = dest_dir / name
    if not dest.exists():
        return dest
    stem, dot, ext = name.rpartition(".")
    n = 2
    while True:
        cand = dest_dir / (f"{stem}-{n}.{ext}" if dot else f"{stem}-{n}")
        if not cand.exists():
            return cand
        n += 1


def archive(dry_run=False):
    if not RESULT.exists():
        print(f"result dir not found: {RESULT}")
        return 0
    moved = skipped = 0
    for entry in sorted(RESULT.iterdir()):
        if not entry.is_file():
            continue
        if entry.name == "INDEX.md":
            continue
        dest_dir = target_dir_for(entry)
        if dest_dir is None:
            skipped += 1
            continue
        dest = unique_dest(dest_dir, entry.name)
        if dry_run:
            print(f"  {entry.name} -> {dest.relative_to(RESULT)}")
        else:
            dest_dir.mkdir(parents=True, exist_ok=True)
            shutil.move(str(entry), str(dest))
        moved += 1
    verb = "would move" if dry_run else "moved"
    print(f"{verb} {moved} file(s); skipped {skipped} file(s) with an unknown extension")
    return moved


def write_index():
    """Write a human- and browser-readable map of the result area."""
    if not RESULT.exists():
        return
    lines = [
        "# 交付区索引",
        "",
        f"更新时间：{datetime.now().strftime('%Y-%m-%d %H:%M')}",
        "",
        "目录按「用途」分区（参照 Windows 已知文件夹的思路），根目录只保留本索引。",
        "",
    ]
    for folder in FOLDER_ORDER:
        d = RESULT / folder
        if not d.exists():
            continue
        n = sum(1 for p in d.rglob("*") if p.is_file())
        if n == 0:
            continue
        label = next((v[0] for v in CATEGORIES.values() if v[1] == folder), folder)
        lines.append(f"- **{folder}/** — {label}（{n} 个文件）")
    for d in sorted(RESULT.iterdir()):
        if not d.is_dir() or d.name in FOLDER_ORDER:
            continue
        n = sum(1 for p in d.rglob("*") if p.is_file())
        lines.append(f"- **{d.name}/** — 专项项目（{n} 个文件）")
    lines.append("")
    (RESULT / "INDEX.md").write_text("\n".join(lines), encoding="utf-8")
    print(f"index written: {RESULT / 'INDEX.md'}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--index-only", action="store_true")
    args = ap.parse_args()
    if not args.index_only:
        archive(dry_run=args.dry_run)
    write_index()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
