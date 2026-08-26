#!/usr/bin/env python3
"""Text-style checks for public documentation.

Semgrep parses code, not Markdown, so the invariants from
checks/semgrep/text-style.yml are checked here for prose.

Standard library only, and no shell: the gate must behave identically on
macOS, Windows, and Linux. An earlier shell version of this check used
`grep -P`, which BSD grep on macOS does not support - it failed and reported
success, so the check silently verified nothing.

Run through the project's virtualenv interpreter:

    .venv/bin/python checks/docs_style.py            # Linux, macOS
    .venv\\Scripts\\python.exe checks\\docs_style.py  # Windows

Exit code is 0 when clean and 1 when anything is found.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

# The project's public documentation, relative to the repository root.
# Directories are searched recursively for *.md; files are checked directly.
DOCS: tuple[str, ...] = ("README.md", "SECURITY.md", "docs")

# Escapes, never literals: a checker for invisible characters must not carry
# any, and an em dash written literally here is indistinguishable from a
# hyphen at a glance.
DASHES = "[\u2013\u2014]"
INVISIBLE = "[\u200b-\u200f\u2060-\u2064\u2066-\u206f]"

CHECKS: tuple[tuple[str, re.Pattern[str], str], ...] = (
    (
        "non-ascii-dash",
        re.compile(DASHES),
        "use an ASCII hyphen instead of an em or en dash",
    ),
    (
        "invisible-character",
        re.compile(INVISIBLE),
        "remove invisible or zero-width characters",
    ),
)


def markdown_files(root: Path) -> list[Path]:
    """Every Markdown file under the given documentation entry."""
    if not root.exists():
        return []
    if root.is_file():
        return [root]
    return sorted(p for p in root.rglob("*.md") if p.is_file())


def scan(path: Path) -> list[tuple[int, str, str, str]]:
    """Findings in one file as (line number, rule id, hint, line text)."""
    try:
        text = path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError) as exc:
        print(f"{path}: cannot read: {exc}", file=sys.stderr)
        return [(0, "unreadable", "file could not be read as UTF-8", "")]

    found = []
    for number, line in enumerate(text.splitlines(), start=1):
        for rule_id, pattern, hint in CHECKS:
            if pattern.search(line):
                found.append((number, rule_id, hint, line.strip()))
    return found


def main() -> int:
    root = Path(__file__).resolve().parent.parent
    total = 0

    for entry in DOCS:
        for path in markdown_files(root / entry):
            for number, rule_id, hint, line in scan(path):
                rel = path.relative_to(root)
                print(f"{rel}:{number}: {rule_id}: {hint}")
                if line:
                    print(f"    {line}")
                total += 1

    if total:
        print(f"\nerror: {total} finding(s) in public documentation", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
