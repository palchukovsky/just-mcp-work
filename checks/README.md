# Semgrep invariant gate

Project-specific checks for invariants `golangci-lint` does not cover: text
hygiene and reference isolation for a public repository. Rules are worded
neutrally - they read as style, security, and architecture checks.

## Run it

```bash
.venv/bin/semgrep --config checks/semgrep/ --error --quiet .
```

Wired into the justfile as `check-semgrep`, a prerequisite of `check-dry`, so
it also runs as part of `check` and `verify`.

## Active (`semgrep/`)

- `text-style.yml` - ASCII punctuation, invisible characters, emoji,
  conversational comments.
- `isolation.yml` - no references to the private half of the workspace
  (machine-local paths, the sibling `tools/` directory).

## Held back (`semgrep-pending/`, real findings, not wired into the gate)

- `tracker-ids.yml` - 1 finding:
  `internal/mcpserver/server_tasks_test.go:57`, a `JMW-30` reference in a
  test comment.
- `errors-go.yml` - 19 findings: 16 discarded-error sites (`Process.Kill`,
  `CloseHandle`, `Handle.Finish`, `StopWithReason`, `os.Remove`,
  `filepath.WalkDir`) and 3 error-dropped-on-return sites
  (`terminate_unix.go` SIGTERM/SIGKILL, `runner/just`). `golangci-lint`
  already tolerates these; `tools/code_style.md` requires a short comment
  documenting an intentionally ignored error, and none of these carry one.

Run a held-back rule manually with
`.venv/bin/semgrep --config checks/semgrep-pending/<file> .`. Move it into
`semgrep/` once its findings are fixed or the rule is narrowed to an agreed
convention.

## Held back (`docs_style.py`, not a Semgrep rule)

Semgrep does not parse Markdown. `docs_style.py` is a standard-library-only
Python check (no shell: `grep -P` is not portable to BSD grep on macOS or to
Windows) for the same punctuation and invisible-character invariants over the
public docs (`README.md`, `SECURITY.md`, `docs/*.md`). 16 findings today, all
`non-ascii-dash` (7 in `README.md`, 9 in `SECURITY.md`); no invisible
characters. Run with `.venv/bin/python checks/docs_style.py` from the
repository root; exits 1 on findings, 0 clean.

## The approval marker

Not currently used: none of the active or held-back rules here are of the
"unmarked fallback" kind that the `// fallback(approved): <reason>` marker
applies to.
