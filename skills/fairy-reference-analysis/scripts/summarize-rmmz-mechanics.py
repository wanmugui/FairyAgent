#!/usr/bin/env python3
"""Read-only RPG Maker data summarizer.

This script intentionally reports aggregate mechanics only. It does not decrypt
assets, extract protected files, or dump dialogue/story text.
"""

from __future__ import annotations

import argparse
import collections
import json
import statistics
from pathlib import Path


def load(path: Path):
    with path.open("r", encoding="utf-8-sig") as handle:
        return json.load(handle)


def find_data_dir(target: Path) -> Path:
    candidates = [
        target / "resources" / "app" / "app" / "data",
        target / "data",
    ]
    for candidate in candidates:
        if (candidate / "System.json").is_file():
            return candidate
    raise SystemExit("Could not find RPG Maker data/System.json under the target")


def number_range(values):
    vals = [float(v) for v in values if isinstance(v, (int, float))]
    if not vals:
        return None
    return {"min": min(vals), "max": max(vals), "avg": round(statistics.mean(vals), 2)}


def summarize(target: Path) -> dict:
    data_dir = find_data_dir(target)
    result: dict = {"data_dir": str(data_dir), "files": {}}
    system = load(data_dir / "System.json")
    result["system"] = {
        "game_title": system.get("gameTitle"),
        "screen": [
            system.get("advanced", {}).get("screenWidth"),
            system.get("advanced", {}).get("screenHeight"),
        ],
        "battle_system": system.get("battleSystem"),
        "side_view": system.get("optSideView"),
        "currency_unit": system.get("currencyUnit"),
        "party_members": len(system.get("partyMembers", [])),
        "menu_commands": sum(1 for value in system.get("menuCommands", []) if value),
        "item_categories": sum(1 for value in system.get("itemCategories", []) if value),
        "has_encrypted_images": system.get("hasEncryptedImages"),
        "has_encrypted_audio": system.get("hasEncryptedAudio"),
        "variables": len(system.get("variables", [])),
        "switches": len(system.get("switches", [])),
    }

    actors = load(data_dir / "Actors.json")
    result["actors"] = {"count": len([a for a in actors if a])}
    classes = load(data_dir / "Classes.json")
    result["classes"] = {"count": len([c for c in classes if c])}
    skills = load(data_dir / "Skills.json")
    result["skills"] = {
        "count": len([s for s in skills if s]),
        "learnable": sum(1 for s in skills if isinstance(s, dict) and s.get("stypeId")),
        "types": collections.Counter(
            str(s.get("stypeId")) for s in skills if isinstance(s, dict) and s.get("stypeId")
        ),
        "max_level": max((int(s.get("maxLevel") or 0) for s in skills if isinstance(s, dict)), default=0),
    }
    items = load(data_dir / "Items.json")
    valid_items = [item for item in items if isinstance(item, dict)]
    result["items"] = {
        "count": len(valid_items),
        "types": collections.Counter(str(item.get("itypeId")) for item in valid_items),
        "price": number_range(item.get("price") for item in valid_items),
        "consumable": sum(1 for item in valid_items if item.get("consumable") is True),
        "key_items": sum(1 for item in valid_items if item.get("keyItem") is True),
    }
    enemies = load(data_dir / "Enemies.json")
    valid_enemies = [enemy for enemy in enemies if isinstance(enemy, dict)]
    result["enemies"] = {
        "count": len(valid_enemies),
        "exp": number_range(enemy.get("exp") for enemy in valid_enemies),
        "gold": number_range(enemy.get("gold") for enemy in valid_enemies),
    }
    states = load(data_dir / "States.json")
    valid_states = [state for state in states if isinstance(state, dict)]
    result["states"] = {
        "count": len(valid_states),
        "restriction": collections.Counter(str(state.get("restriction")) for state in valid_states),
        "remove_by_damage": sum(1 for state in valid_states if state.get("removeByDamage") is True),
    }
    troops = load(data_dir / "Troops.json")
    valid_troops = [troop for troop in troops if isinstance(troop, dict)]
    result["troops"] = {
        "count": len(valid_troops),
        "with_battle_events": sum(1 for troop in valid_troops if troop.get("pages")),
    }
    common_events = load(data_dir / "CommonEvents.json")
    valid_common = [event for event in common_events if isinstance(event, dict)]
    result["common_events"] = {
        "count": len(valid_common),
        "commands": sum(len(event.get("list", [])) for event in valid_common),
        "triggers": collections.Counter(str(event.get("trigger")) for event in valid_common),
    }
    map_files = sorted(data_dir.glob("Map[0-9][0-9][0-9].json"))
    result["maps"] = {"count": len(map_files)}
    return result


def write_markdown(summary: dict, target: Path, output: Path) -> None:
    lines = [
        "# RPG Maker Mechanics Summary",
        "",
        f"- Target: `{target}`",
        f"- Data: `{summary['data_dir']}`",
        "- Mode: aggregate statistics only; no dialogue, story, or raw asset extraction",
        "",
        "## System",
        "",
        "```json",
        json.dumps(summary["system"], ensure_ascii=False, indent=2),
        "```",
        "",
        "## Aggregate counts",
        "",
        "| Area | Count |",
        "|---|---:|",
        f"| Actors | {summary['actors']['count']} |",
        f"| Classes | {summary['classes']['count']} |",
        f"| Skills | {summary['skills']['count']} |",
        f"| Items | {summary['items']['count']} |",
        f"| Enemies | {summary['enemies']['count']} |",
        f"| States | {summary['states']['count']} |",
        f"| Troops | {summary['troops']['count']} |",
        f"| Common events | {summary['common_events']['count']} |",
        f"| Maps | {summary['maps']['count']} |",
        "",
        "## Mechanics indicators",
        "",
        "```json",
        json.dumps(
            {
                "skills": summary["skills"],
                "items": summary["items"],
                "enemies": summary["enemies"],
                "states": summary["states"],
                "troops": summary["troops"],
                "common_events": summary["common_events"],
            },
            ensure_ascii=False,
            indent=2,
            default=lambda value: dict(value) if isinstance(value, collections.Counter) else str(value),
        ),
        "```",
        "",
        "## Next steps",
        "",
        "1. Validate inferred formulas with controlled gameplay experiments.",
        "2. Keep observed facts separate from inferred rules.",
        "3. Re-express the mechanics as an original design brief.",
    ]
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text("\n".join(lines) + "\n", encoding="utf-8")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("target", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    target = args.target.resolve()
    summary = summarize(target)
    write_markdown(summary, target, args.output)
    print(args.output)


if __name__ == "__main__":
    main()