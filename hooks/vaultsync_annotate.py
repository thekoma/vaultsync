#!/usr/bin/env python3
"""Pre-commit hook that auto-adds vaultsync/watch annotations.

Scans YAML files for vault:secret/data/... patterns and adds/updates the
vaultsync/watch annotation on each document's metadata so the vaultSync
controller knows which secrets to watch.

Usage:
    vaultsync_annotate.py [--check] FILE [FILE ...]

Exits 0 if nothing needed changing.
Exits 1 if files were modified (fix mode) or need modification (check mode).
"""

from __future__ import annotations

import argparse
import re
import sys
from typing import List, Optional, Tuple

# Pattern: vault:secret/data/PATH#key  or  ${vault:secret/data/PATH#key}
VAULT_REF_RE = re.compile(r"""\$?\{?vault:secret/data/([^#\}"'\s]+)#[^}"'\s]+""")

# Detect the "mutate: skip" annotation (uncommented)
MUTATE_SKIP_RE = re.compile(
    r"""^\s+vault\.security\.banzaicloud\.io/mutate:\s*["']?skip["']?\s*$""",
    re.MULTILINE,
)

ANNOTATION_KEY = "vaultsync/watch"


def extract_vault_paths(text: str) -> List[str]:
    """Return sorted, deduplicated vault paths found in *text*."""
    paths = set()
    for m in VAULT_REF_RE.finditer(text):
        paths.add("secret/data/" + m.group(1))
    return sorted(paths)


def find_annotation_value(doc: str) -> Optional[str]:
    """Return the current vaultsync/watch value in *doc*, or None."""
    # Match the annotation line allowing for quoting
    m = re.search(
        r"""^\s+vaultsync/watch:\s*["']?([^"'\n]*)["']?\s*$""",
        doc,
        re.MULTILINE,
    )
    if m:
        return m.group(1).strip()
    return None


def _indent_of(line: str) -> int:
    return len(line) - len(line.lstrip())


def add_or_update_annotation(doc: str, desired: str) -> str:
    """Return *doc* with the vaultsync/watch annotation set to *desired*.

    Handles three cases:
      1. Annotation already present -> replace value
      2. annotations: block exists   -> append line
      3. No annotations: block       -> create one after metadata:
    """
    # --- Case 1: annotation already present, just needs updating -----------
    pattern = re.compile(
        r"""^(\s+)(vaultsync/watch:\s*)["']?[^"'\n]*["']?\s*$""",
        re.MULTILINE,
    )
    m = pattern.search(doc)
    if m:
        indent = m.group(1)
        return pattern.sub(f'{indent}vaultsync/watch: "{desired}"', doc)

    # --- Case 2: annotations: block exists ---------------------------------
    # Find "  annotations:" under metadata.  We look for the pattern:
    #   metadata:\n
    #     ...(name/namespace/labels lines)...\n
    #     annotations:\n
    ann_re = re.compile(r"^(\s+)(annotations:\s*)$", re.MULTILINE)
    m = ann_re.search(doc)
    if m:
        ann_indent = m.group(1)
        ann_end = m.end()
        # Determine the indent used by existing annotation keys by peeking at
        # the next non-blank line.
        rest = doc[ann_end:]
        next_line_m = re.match(r"\n(\s+)\S", rest)
        if next_line_m:
            key_indent = next_line_m.group(1)
        else:
            key_indent = ann_indent + "  "
        new_line = f'\n{key_indent}vaultsync/watch: "{desired}"'
        return doc[:ann_end] + new_line + doc[ann_end:]

    # --- Case 3: no annotations: block, create one after metadata: ----------
    meta_re = re.compile(r"^(\s*)(metadata:\s*)$", re.MULTILINE)
    m = meta_re.search(doc)
    if m:
        meta_indent = m.group(1)
        child_indent = meta_indent + "  "
        ann_key_indent = child_indent + "  "
        insert_pos = m.end()
        # We need to find the right place: after existing metadata children
        # (name, namespace, labels, etc.) but before spec: or other top-level keys.
        # Walk lines after metadata: to find the last metadata child.
        lines = doc[insert_pos:].split("\n")
        offset = insert_pos
        last_child_end = insert_pos
        for i, line in enumerate(lines):
            if i == 0 and line.strip() == "":
                # The rest of the metadata: line itself (newline)
                offset += len(line) + 1
                last_child_end = offset
                continue
            if i == 0:
                offset += len(line) + 1
                last_child_end = offset
                continue
            if line.strip() == "":
                offset += len(line) + 1
                continue
            ind = _indent_of(line)
            if ind > len(meta_indent):
                offset += len(line) + 1
                last_child_end = offset
            else:
                break

        # last_child_end points right after the newline of the last metadata
        # child line, so we don't need a leading newline.
        new_block = (
            f"{child_indent}annotations:"
            f'\n{ann_key_indent}vaultsync/watch: "{desired}"'
            f"\n"
        )
        return doc[:last_child_end] + new_block + doc[last_child_end:]

    # No metadata: block at all -- unlikely, return unchanged
    return doc


def process_document(doc: str) -> Tuple[str, bool]:
    """Process a single YAML document.

    Returns (possibly_modified_doc, was_changed).
    """
    # Skip documents that have "mutate: skip" (parent apps)
    if MUTATE_SKIP_RE.search(doc):
        return doc, False

    paths = extract_vault_paths(doc)
    if not paths:
        return doc, False

    desired = ",".join(paths)
    current = find_annotation_value(doc)
    if current == desired:
        return doc, False

    new_doc = add_or_update_annotation(doc, desired)
    return new_doc, True


def process_file(filepath: str, check: bool) -> bool:
    """Process a single file.  Returns True if changes were made / needed."""
    try:
        with open(filepath, "r") as f:
            content = f.read()
    except (OSError, IOError) as e:
        print(f"warning: cannot read {filepath}: {e}", file=sys.stderr)
        return False

    # Split on YAML document separator, keeping track of separators
    # We split on lines that are exactly "---" (possibly with trailing whitespace)
    parts = re.split(r"(^---\s*$)", content, flags=re.MULTILINE)

    changed = False
    new_parts: List[str] = []
    for part in parts:
        if re.match(r"^---\s*$", part):
            new_parts.append(part)
            continue
        new_doc, doc_changed = process_document(part)
        if doc_changed:
            changed = True
        new_parts.append(new_doc)

    if not changed:
        return False

    new_content = "".join(new_parts)

    if check:
        # Report what would change
        paths_found = extract_vault_paths(content)
        print(
            f"{filepath}: needs vaultsync/watch annotation "
            f"({','.join(paths_found)})",
            file=sys.stderr,
        )
    else:
        with open(filepath, "w") as f:
            f.write(new_content)
        paths_found = extract_vault_paths(content)
        print(
            f"{filepath}: added/updated vaultsync/watch annotation "
            f"({','.join(paths_found)})",
            file=sys.stderr,
        )

    return True


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Add vaultsync/watch annotations to YAML files with vault references."
    )
    parser.add_argument(
        "--check",
        action="store_true",
        help="Check mode: report missing annotations without modifying files.",
    )
    parser.add_argument("files", nargs="*", help="YAML files to process.")
    args = parser.parse_args()

    if not args.files:
        return 0

    any_changed = False
    for filepath in args.files:
        if process_file(filepath, check=args.check):
            any_changed = True

    return 1 if any_changed else 0


if __name__ == "__main__":
    raise SystemExit(main())
