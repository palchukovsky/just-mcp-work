#!/usr/bin/env python3
"""Release notes: commit them for the next version and check them before publishing."""

from __future__ import annotations

import argparse
import shutil
import subprocess
from pathlib import Path

from dev import ROOT, current_branch, ensure_clean_worktree, next_version, run

RELEASE_NOTES = ROOT / "RELEASE_NOTES.md"


def check(version: str, notes: Path) -> None:
    if not notes.is_file():
        raise SystemExit(f"release notes file {notes} is required")
    lines = notes.read_text(encoding="utf-8").splitlines()
    marker = f"<!-- release-version: {version} -->"
    if not lines or lines[0] != marker:
        raise SystemExit(f"{notes} must start with {marker}")
    if not any(line.strip() for line in lines[1:]):
        raise SystemExit(f"{notes} must contain release notes, not just whitespace")


def prepare(kind: str, notes: Path) -> None:
    """Commit the notes for the next version and push the branch.

    The release tag is created on this commit afterwards, so the pipeline
    publishes the notes the tag carries.
    """
    if kind not in {"patch", "minor", "major"}:
        raise SystemExit("release kind must be patch, minor, or major")
    if not notes.is_absolute():
        raise SystemExit("release notes file must be an absolute path")
    ensure_clean_worktree()
    check(next_version(kind), notes)
    shutil.copyfile(notes, RELEASE_NOTES)
    run(["git", "add", "--", RELEASE_NOTES.name])
    staged = subprocess.run(["git", "diff", "--cached", "--quiet"], cwd=ROOT)
    if staged.returncode != 0:
        run(["git", "commit", "-m", f"Add release notes for {next_version(kind)}"])
    run(["git", "push", "origin", "HEAD:" + current_branch()])


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    check_parser = commands.add_parser("check")
    check_parser.add_argument("version")
    check_parser.add_argument("notes", nargs="?", type=Path, default=RELEASE_NOTES)
    prepare_parser = commands.add_parser("prepare")
    prepare_parser.add_argument("kind")
    prepare_parser.add_argument("notes", type=Path)
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    if args.command == "check":
        check(args.version, args.notes)
    else:
        prepare(args.kind, args.notes)


if __name__ == "__main__":
    main()
