# Semgrep invariant gate

Project-specific checks for invariants `golangci-lint` does not cover: text
hygiene, reference isolation, and swallowed errors, for a public repository.
Rules are worded neutrally - they read as style, security, and architecture
checks.

## Run it

```bash
.venv/bin/semgrep --config checks/semgrep/ --error --quiet .
```

Wired into the justfile as `check-semgrep`, a prerequisite of `check-dry`, so
it also runs as part of `check` and `verify`, locally and in CI.

The generic rules (`text-style.yml`, `isolation.yml`, `ascii-punctuation.yml`,
`tracker-ids.yml`) carry no `paths.include`: they scan every text file Semgrep
can read across the whole tree (`.go`, `.py`, `.md`, `.yml`, `.json`,
justfiles, `.gitignore`/`.gitattributes`), not only source files. `errors-go.yml`
is Go-specific by `languages: [go]`, not by an include list. Each rule's own
`paths.exclude` is what narrows it, and every exclusion is documented in the
rule file it appears in.

## Rules (`semgrep/`)

- `text-style.yml` - invisible characters, emoji, conversational comments.
- `isolation.yml` - no references to the private half of the workspace
  (machine-local paths, the sibling `tools/` directory).
- `ascii-punctuation.yml` - ASCII punctuation only, no em/en dashes.
- `tracker-ids.yml` - no private-tracker issue IDs in source or docs.
- `errors-go.yml` - `discarded-error` catches one-result and two-result all-blank
  assignments (`_ = call(...)` and `_, _ = call(...)`);
  `error-dropped-on-return` catches a checked error path that returns without
  the error.

## Documented exceptions

All active rules must pass. `discarded-error` excludes direct `.Close()` calls
as deferred cleanup. Every other ignored error needs a short rationale and an
exact `nosemgrep` suppression for that rule. This is reserved for best-effort
cleanup whose failure cannot change the operation's outcome, not for ordinary
error handling.

Mixed-result assignments, such as `value, _ := call(...)` or
`value, _ = call(...)`, are not matched. All-blank assignments with three or
more results, such as `_, _, _ = call(...)`, are also not matched.

## The approval marker

Not currently used: none of the rules here are of the "unmarked fallback"
kind that the `// fallback(approved): <reason>` marker applies to.
