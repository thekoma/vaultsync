#!/usr/bin/env python3
"""Pre-commit hook that auto-adds vaultsync/watch annotations.

Scans YAML files for vault:MOUNT/data/PATH#key patterns and adds/updates
the vaultsync/watch annotation on each document's metadata so the vaultSync
controller knows which secrets to watch. Works with any Vault KV v2 mount
name (secret, kv, custom-mount, etc.).

Usage:
    vaultsync_annotate.py [--check] [--watch-annotation KEY] FILE [FILE ...]

Exits 0 if nothing needed changing.
Exits 1 if files were modified (fix mode) or need modification (check mode).
"""

from __future__ import annotations

import argparse
import re
import sys
from typing import List, Optional, Tuple

# Pattern: vault:MOUNT/data/PATH#key  or  ${vault:MOUNT/data/PATH#key}
# Captures the full path including mount (e.g., "secret/data/litellm", "kv/data/myapp")
VAULT_REF_RE = re.compile(r"""\$?\{?vault:([^#\}"'\s]+/data/[^#\}"'\s]+)#[^}"'\s]+""")

# Detect the "mutate: skip" annotation (uncommented)
MUTATE_SKIP_RE = re.compile(
    r"""^\s+vault\.security\.banzaicloud\.io/mutate:\s*["']?skip["']?\s*$""",
    re.MULTILINE,
)

DEFAULT_ANNOTATION_KEY = "vaultsync/watch"
DEFAULT_TRIGGER_KEY = "vaultsync/trigger"


def extract_vault_paths(text: str) -> List[str]:
    """Return sorted, deduplicated vault paths found in *text*.

    Captures the full mount/data/path (e.g., 'secret/data/litellm',
    'kv/data/myapp') so the annotation works with any KV v2 mount name.
    """
    paths = set()
    for m in VAULT_REF_RE.finditer(text):
        paths.add(m.group(1))
    return sorted(paths)


def find_annotation_value(doc: str, annotation_key: str = DEFAULT_ANNOTATION_KEY) -> Optional[str]:
    """Return the current annotation value in *doc*, or None."""
    # Match the annotation line allowing for quoting
    escaped_key = re.escape(annotation_key)
    m = re.search(
        rf"""^\s+{escaped_key}:\s*["']?([^"'\n]*)["']?\s*$""",
        doc,
        re.MULTILINE,
    )
    if m:
        return m.group(1).strip()
    return None


def _indent_of(line: str) -> int:
    return len(line) - len(line.lstrip())


def add_or_update_annotation(doc: str, desired: str, annotation_key: str = DEFAULT_ANNOTATION_KEY) -> str:
    """Return *doc* with the watch annotation set to *desired*.

    Handles three cases:
      1. Annotation already present -> replace value
      2. annotations: block exists   -> append line
      3. No annotations: block       -> create one after metadata:
    """
    escaped_key = re.escape(annotation_key)
    # --- Case 1: annotation already present, just needs updating -----------
    pattern = re.compile(
        rf"""^(\s+)({escaped_key}:\s*)["']?[^"'\n]*["']?\s*$""",
        re.MULTILINE,
    )
    m = pattern.search(doc)
    if m:
        indent = m.group(1)
        return pattern.sub(f'{indent}{annotation_key}: "{desired}"', doc)

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
        new_line = f'\n{key_indent}{annotation_key}: "{desired}"'
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
            f'\n{ann_key_indent}{annotation_key}: "{desired}"'
            f"\n"
        )
        return doc[:last_child_end] + new_block + doc[last_child_end:]

    # No metadata: block at all -- unlikely, return unchanged
    return doc


def is_k8s_manifest(doc: str) -> bool:
    """Return True if the document looks like a Kubernetes manifest.

    A K8s manifest has both 'apiVersion:' and 'kind:' as top-level keys.
    Plain Helm values files or other config files do not.
    """
    has_api = re.search(r"^apiVersion:\s", doc, re.MULTILINE) is not None
    has_kind = re.search(r"^kind:\s", doc, re.MULTILINE) is not None
    return has_api and has_kind


# Matches an annotations: block that contains vault.security.banzaicloud.io/
# but NOT mutate: skip
BANKVAULTS_ANN_RE = re.compile(
    r"""vault\.security\.banzaicloud\.io/(?!mutate)""",
)


def annotate_values_file(doc: str, desired: str, annotation_key: str) -> Tuple[str, bool, bool]:
    """Add watch annotation to annotation blocks in Helm values files.

    Finds every 'annotations:' block that contains vault.security.banzaicloud.io/
    annotations (but not mutate: skip) and inserts the watch annotation if missing.
    Returns (modified_doc, was_changed, had_bankvaults_blocks).
    """
    lines = doc.split("\n")
    new_lines = []
    changed = False
    found_bankvaults_block = False
    i = 0
    escaped_key = re.escape(annotation_key)

    while i < len(lines):
        line = lines[i]
        new_lines.append(line)

        # Look for "annotations:" lines (at any indent level)
        ann_match = re.match(r"^(\s+)(annotations:\s*)$", line)
        if not ann_match:
            i += 1
            continue

        ann_indent = len(ann_match.group(1))

        # Collect the annotation block lines (deeper indent than annotations:)
        block_start = i + 1
        block_end = block_start
        while block_end < len(lines):
            next_line = lines[block_end]
            if next_line.strip() == "":
                block_end += 1
                continue
            if _indent_of(next_line) > ann_indent:
                block_end += 1
            else:
                break

        block_lines = lines[block_start:block_end]
        block_text = "\n".join(block_lines)

        # Check if this block has vault.security.banzaicloud.io/ (no skip)
        has_bankvaults = BANKVAULTS_ANN_RE.search(block_text)
        has_skip = re.search(r"""mutate:\s*["']?skip""", block_text)
        already_has = re.search(rf"""^\s+{escaped_key}:""", block_text, re.MULTILINE)

        if has_bankvaults and not has_skip:
            found_bankvaults_block = True

        if has_bankvaults and not has_skip and not already_has:
            # Determine indent from the first annotation key in the block
            key_indent = None
            for bl in block_lines:
                if bl.strip():
                    key_indent = " " * _indent_of(bl)
                    break
            if key_indent is None:
                key_indent = " " * (ann_indent + 2)

            # Insert the watch annotation as the first key in the block
            new_lines.append(f'{key_indent}{annotation_key}: "{desired}"')
            changed = True

        # Append the rest of the block
        for j in range(block_start, block_end):
            new_lines.append(lines[j])
        i = block_end

    return "\n".join(new_lines), changed, found_bankvaults_block


def ensure_trigger_annotation(doc: str, trigger_key: str) -> Tuple[str, bool]:
    """Add trigger annotation (empty value) next to every watch annotation.

    For ArgoCD to "own" the trigger field, it must be in the rendered manifest.
    This adds 'vaultsync/trigger: ""' on the line after every vaultsync/watch
    if not already present.
    Returns (modified_doc, was_changed).
    """
    escaped_trigger = re.escape(trigger_key)
    if re.search(rf"^\s+{escaped_trigger}:", doc, re.MULTILINE):
        return doc, False  # already present

    escaped_watch = re.escape(DEFAULT_ANNOTATION_KEY)
    # Find every watch annotation line and insert trigger after it
    lines = doc.split("\n")
    new_lines = []
    changed = False
    for line in lines:
        new_lines.append(line)
        m = re.match(rf'^(\s+){escaped_watch}:\s', line)
        if m:
            indent = m.group(1)
            new_lines.append(f'{indent}{trigger_key}: ""')
            changed = True
    return "\n".join(new_lines), changed


def process_document(doc: str, annotation_key: str = DEFAULT_ANNOTATION_KEY, trigger_key: str = DEFAULT_TRIGGER_KEY) -> Tuple[str, bool, List[str]]:
    """Process a single YAML document.

    Returns (possibly_modified_doc, was_changed, unhandled_paths).
    unhandled_paths is non-empty when vault refs are found in a non-manifest
    document that has no Bank-Vaults annotation blocks to attach to.
    """
    # Skip documents that have "mutate: skip" at the top level (parent apps)
    if MUTATE_SKIP_RE.search(doc):
        return doc, False, []

    paths = extract_vault_paths(doc)
    if not paths:
        return doc, False, []

    desired = ",".join(paths)
    changed = False

    # Case 1: Kubernetes manifest — annotate metadata.annotations as before
    if is_k8s_manifest(doc):
        current = find_annotation_value(doc, annotation_key)
        if current != desired:
            doc = add_or_update_annotation(doc, desired, annotation_key)
            changed = True
        # Ensure trigger annotation exists alongside watch
        doc, trigger_changed = ensure_trigger_annotation(doc, trigger_key)
        changed = changed or trigger_changed
        if changed:
            return doc, True, []
        return doc, False, []

    # Case 2: Non-manifest (values file) — annotate Bank-Vaults annotation blocks
    doc, values_changed, had_blocks = annotate_values_file(doc, desired, annotation_key)
    if had_blocks:
        # Also ensure trigger annotation in values files
        doc, trigger_changed = ensure_trigger_annotation(doc, trigger_key)
        changed = values_changed or trigger_changed
        if changed:
            return doc, True, []
        return doc, False, []

    # Case 3: Non-manifest with no Bank-Vaults annotation blocks — warn
    return doc, False, paths


def process_file(filepath: str, check: bool, annotation_key: str = DEFAULT_ANNOTATION_KEY, trigger_key: str = DEFAULT_TRIGGER_KEY) -> bool:
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
    all_unhandled: List[str] = []
    new_parts: List[str] = []
    for part in parts:
        if re.match(r"^---\s*$", part):
            new_parts.append(part)
            continue
        new_doc, doc_changed, unhandled = process_document(part, annotation_key, trigger_key)
        if doc_changed:
            changed = True
        if unhandled:
            all_unhandled.extend(unhandled)
        new_parts.append(new_doc)

    # Warn about vault refs in non-manifest files (values files, configs, etc.)
    if all_unhandled:
        unique = sorted(set(all_unhandled))
        print(
            f"WARNING: {filepath}: vault references found but file is not a "
            f"Kubernetes manifest (no apiVersion/kind). "
            f"Ensure the parent Application CR has {annotation_key} for: "
            f"{','.join(unique)}",
            file=sys.stderr,
        )

    if not changed:
        return False

    new_content = "".join(new_parts)

    if check:
        paths_found = extract_vault_paths(content)
        print(
            f"{filepath}: needs {annotation_key} annotation "
            f"({','.join(paths_found)})",
            file=sys.stderr,
        )
    else:
        with open(filepath, "w") as f:
            f.write(new_content)
        paths_found = extract_vault_paths(content)
        print(
            f"{filepath}: added/updated {annotation_key} annotation "
            f"({','.join(paths_found)})",
            file=sys.stderr,
        )

    return True


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Add vaultsync/watch annotations to YAML files with vault references.",
        epilog="""
examples:
  # Fix mode (default) — add missing annotations in-place:
  %(prog)s deployment.yaml secret.yaml

  # Check mode — report what needs fixing without modifying:
  %(prog)s --check deployment.yaml secret.yaml

  # Use with pre-commit (in .pre-commit-config.yaml):
  #   - repo: https://github.com/thekoma/vaultsync
  #     rev: 2026.4.2
  #     hooks:
  #       - id: vaultsync-annotate
  #
  # For check-only in CI, pass --check via args:
  #       - id: vaultsync-annotate
  #         args: [--check]

behavior:
  Scans YAML files for Bank-Vaults webhook references matching the pattern
  vault:MOUNT/data/PATH#key (e.g., vault:secret/data/myapp#password).

  For each Kubernetes resource document containing such references, it
  adds or updates a vaultsync/watch annotation with the deduplicated,
  sorted list of vault paths. This annotation tells the vaultSync
  controller which Vault secrets to monitor for version changes.

  Resources with 'vault.security.banzaicloud.io/mutate: skip' are
  excluded (these are typically ArgoCD parent apps that should not be
  watched directly).

  Multi-document YAML files (separated by ---) are supported; each
  document is processed independently.

exit codes:
  0  No changes needed (all annotations are correct or no vault refs found)
  1  Files were modified (fix mode) or need modification (check mode)
""",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument(
        "--check",
        action="store_true",
        help="Report missing/incorrect annotations without modifying files.",
    )
    parser.add_argument(
        "--watch-annotation",
        default=DEFAULT_ANNOTATION_KEY,
        help=f"Annotation key to use for vault watch (default: {DEFAULT_ANNOTATION_KEY}).",
    )
    parser.add_argument(
        "--trigger-annotation",
        default=DEFAULT_TRIGGER_KEY,
        help=f"Annotation key for the drift trigger (default: {DEFAULT_TRIGGER_KEY}).",
    )
    parser.add_argument("files", nargs="*", help="YAML files to process.")
    args = parser.parse_args()

    if not args.files:
        return 0

    any_changed = False
    for filepath in args.files:
        if process_file(filepath, check=args.check, annotation_key=args.watch_annotation, trigger_key=args.trigger_annotation):
            any_changed = True

    return 1 if any_changed else 0


if __name__ == "__main__":
    raise SystemExit(main())
