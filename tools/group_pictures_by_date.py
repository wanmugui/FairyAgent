#!/usr/bin/env python3
"""Group loose images inside Pictures/ by capture date.

Windows Photos does the same thing: a folder of a few hundred images
becomes unreadable flat, but "by date" makes it scannable. Here the
"date" is the file's mtime, which for our screenshots is the day the
work happened.

Idempotent: files already inside a dated sub-folder are left alone.

Usage:
    python3 tools/group_pictures_by_date.py [--dry-run]
"""

import argparse
import shutil
import sys
from datetime import datetime
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
PICTURES = REPO / "workspace" / "result" / "Pictures"

# Sub-folders we created earlier (by subject prefix) stay as they are.
PREFIX_GROUPS = {"zzz", "fairy", "agent"}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dry-run", action="store_true")
    args = ap.parse_args()

    if not PICTURES.exists():
        print(f"not found: {PICTURES}")
        return 0

    moved = 0
    for entry in sorted(PICTURES.iterdir()):
        if not entry.is_file():
            continue
        try:
            stamp = datetime.fromtimestamp(entry.stat().st_mtime)
        except OSError:
            continue
        day = stamp.strftime("%Y-%m-%d")
        dest_dir = PICTURES / day
        dest = dest_dir / entry.name
        if dest.exists():
            stem, dot, ext = entry.name.rpartition(".")
            n = 2
            while True:
                cand = dest_dir / (f"{stem}-{n}.{ext}" if dot else f"{stem}-{n}")
                if not cand.exists():
                    dest = cand
                    break
                n += 1
        if args.dry_run:
            print(f"  {entry.name} -> {day}/")
        else:
            dest_dir.mkdir(parents=True, exist_ok=True)
            shutil.move(str(entry), str(dest))
        moved += 1

    verb = "would move" if args.dry_run else "moved"
    print(f"{verb} {moved} image(s) into date sub-folders")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
