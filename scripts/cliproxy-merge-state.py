#!/usr/bin/env python3
"""Merge a drained CLIProxyAPI slot into a clone of the active slot.

The three directories are snapshots of the same auth-dir at cutover time:
BASE is the old slot at cutover, OLD is that slot after draining, and TARGET
is a fresh clone of the current active slot. Additive usage counters are merged
as deltas; credential and cooldown files use a conservative three-way merge.
"""

from __future__ import annotations

import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import shutil
from typing import Any

ACCESS_STORE = Path("access-keys.store")
USAGE_MONITOR = Path(".usage-monitor/usage-monitor.json")
IGNORED_PARTS = {"logs", "error-logs"}


def load_json(path: Path, default: Any) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        return copy.deepcopy(default)


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temp = path.with_name(path.name + ".merge.tmp")
    temp.write_text(json.dumps(value, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    os.chmod(temp, 0o600)
    temp.replace(path)


def digest(path: Path) -> str | None:
    try:
        data = path.read_bytes()
    except FileNotFoundError:
        return None
    return hashlib.sha256(data).hexdigest()


def max_text(left: Any, right: Any) -> Any:
    if not left:
        return right
    if not right:
        return left
    return max(str(left), str(right))


def merge_counter_tree(base: Any, old: Any, target: Any) -> Any:
    if isinstance(old, dict) or isinstance(base, dict) or isinstance(target, dict):
        base_map = base if isinstance(base, dict) else {}
        old_map = old if isinstance(old, dict) else {}
        target_map = target if isinstance(target, dict) else {}
        result = copy.deepcopy(target_map)
        for key in set(base_map) | set(old_map):
            result[key] = merge_counter_tree(base_map.get(key), old_map.get(key), target_map.get(key))
        return result
    if isinstance(old, (int, float)) and not isinstance(old, bool):
        baseline = base if isinstance(base, (int, float)) and not isinstance(base, bool) else 0
        current = target if isinstance(target, (int, float)) and not isinstance(target, bool) else 0
        return max(0, current + old - baseline)
    if isinstance(old, str):
        if isinstance(base, str) and old == base:
            return target if target is not None else old
        if isinstance(target, str) and isinstance(base, str) and target != base:
            return max_text(target, old)
        return old
    if old != base and target == base:
        return copy.deepcopy(old)
    return copy.deepcopy(target if target is not None else old)


def entity_map(items: Any, key_fields: tuple[str, ...]) -> dict[tuple[str, ...], dict[str, Any]]:
    result: dict[tuple[str, ...], dict[str, Any]] = {}
    if not isinstance(items, list):
        return result
    for item in items:
        if not isinstance(item, dict):
            continue
        key = tuple(str(item.get(field, "")) for field in key_fields)
        if all(key):
            result[key] = item
    return result


def without_usage(item: dict[str, Any]) -> dict[str, Any]:
    result = copy.deepcopy(item)
    result.pop("usage", None)
    return result


def merge_access_entities(base_items: Any, old_items: Any, target_items: Any, key_fields: tuple[str, ...]) -> list[dict[str, Any]]:
    base_map = entity_map(base_items, key_fields)
    old_map = entity_map(old_items, key_fields)
    target_map = entity_map(target_items, key_fields)
    result = copy.deepcopy(target_map)

    for key in set(base_map) | set(old_map):
        base_item = base_map.get(key)
        old_item = old_map.get(key)
        target_item = result.get(key)
        if old_item is None:
            if base_item is not None and target_item == base_item:
                result.pop(key, None)
            continue
        if base_item is None:
            if target_item is None:
                result[key] = copy.deepcopy(old_item)
            continue
        if target_item is None:
            continue

        merged = copy.deepcopy(target_item)
        old_meta = without_usage(old_item)
        base_meta = without_usage(base_item)
        target_meta = without_usage(target_item)
        if old_meta != base_meta and target_meta == base_meta:
            merged = copy.deepcopy(old_meta)
            if "usage" in target_item:
                merged["usage"] = copy.deepcopy(target_item["usage"])
        merged["usage"] = merge_counter_tree(
            base_item.get("usage", {}), old_item.get("usage", {}), target_item.get("usage", {})
        )
        result[key] = merged

    return sorted(result.values(), key=lambda item: tuple(str(item.get(field, "")) for field in key_fields))


def merge_access_store(base_path: Path, old_path: Path, target_path: Path) -> None:
    base = load_json(base_path, {"keys": [], "groups": []})
    old = load_json(old_path, {"keys": [], "groups": []})
    target = load_json(target_path, {"keys": [], "groups": []})
    if isinstance(base, list):
        base = {"keys": base, "groups": []}
    if isinstance(old, list):
        old = {"keys": old, "groups": []}
    if isinstance(target, list):
        target = {"keys": target, "groups": []}
    merged = copy.deepcopy(target)
    merged["keys"] = merge_access_entities(base.get("keys"), old.get("keys"), target.get("keys"), ("id",))
    merged["groups"] = merge_access_entities(base.get("groups"), old.get("groups"), target.get("groups"), ("name",))
    write_json(target_path, merged)


def rows_to_map(rows: Any) -> dict[tuple[str, str], dict[str, Any]]:
    return entity_map(rows, ("provider", "model"))


def merge_usage_monitor(base_path: Path, old_path: Path, target_path: Path) -> None:
    if not old_path.exists():
        return
    base = load_json(base_path, {})
    old = load_json(old_path, {})
    target = load_json(target_path, {})
    merged = merge_counter_tree(base, old, target)
    base_rows = rows_to_map(base.get("rows", []))
    old_rows = rows_to_map(old.get("rows", []))
    target_rows = rows_to_map(target.get("rows", []))
    rows: dict[tuple[str, str], dict[str, Any]] = copy.deepcopy(target_rows)
    for key in set(base_rows) | set(old_rows):
        if key not in old_rows:
            continue
        rows[key] = merge_counter_tree(base_rows.get(key, {}), old_rows[key], target_rows.get(key, {}))
    merged["rows"] = sorted(rows.values(), key=lambda row: (str(row.get("provider", "")), str(row.get("model", ""))))
    since_values = [str(value) for value in (base.get("since"), old.get("since"), target.get("since")) if value]
    if since_values:
        merged["since"] = min(since_values)
    merged["updated_at"] = max_text(target.get("updated_at"), old.get("updated_at"))
    write_json(target_path, merged)


def merge_regular_files(base_dir: Path, old_dir: Path, target_dir: Path) -> None:
    relative_paths: set[Path] = set()
    for root in (base_dir, old_dir):
        if not root.exists():
            continue
        for path in root.rglob("*"):
            if path.is_file():
                rel = path.relative_to(root)
                if rel in {ACCESS_STORE, USAGE_MONITOR} or any(part in IGNORED_PARTS for part in rel.parts):
                    continue
                if rel.name.endswith(".tmp") or ".merge.tmp" in rel.name:
                    continue
                relative_paths.add(rel)

    for rel in sorted(relative_paths):
        base_path = base_dir / rel
        old_path = old_dir / rel
        target_path = target_dir / rel
        base_hash = digest(base_path)
        old_hash = digest(old_path)
        target_hash = digest(target_path)
        if old_hash == base_hash:
            continue
        if old_hash is None:
            if target_hash == base_hash:
                target_path.unlink(missing_ok=True)
            continue
        if target_hash in (None, base_hash):
            target_path.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(old_path, target_path)
            continue
        if old_path.stat().st_mtime_ns > target_path.stat().st_mtime_ns:
            shutil.copy2(old_path, target_path)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("base", type=Path)
    parser.add_argument("old", type=Path)
    parser.add_argument("target", type=Path)
    args = parser.parse_args()

    args.target.mkdir(parents=True, exist_ok=True)
    merge_regular_files(args.base, args.old, args.target)
    if (args.old / ACCESS_STORE).exists() or (args.base / ACCESS_STORE).exists():
        merge_access_store(args.base / ACCESS_STORE, args.old / ACCESS_STORE, args.target / ACCESS_STORE)
    merge_usage_monitor(args.base / USAGE_MONITOR, args.old / USAGE_MONITOR, args.target / USAGE_MONITOR)


if __name__ == "__main__":
    main()
