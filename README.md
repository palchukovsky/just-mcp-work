# just-mcp-work

[![Verify][verify-badge]][verify-workflow]
[![Release][release-badge]][release-workflow]
[![Latest release][release-version-badge]][releases]
[![Go][go-badge]][go-cache]
[![License: MIT][license-badge]][license]

> Just work with your workspace — over MCP.

Your coding agent spends context reading build files and re-reading logs it
already ran. `just-mcp-work` is a small local MCP server that gives it one way
to find and run everything a workspace can run, and answers with a short receipt
instead of a wall of output.

- **No build files in context.** Go modules, Just, CMake, Docker, and GNU Make
  projects nested anywhere in the workspace are discovered on demand. The agent
  asks for one task and gets that one task.
- **Output only when it is wanted.** A run answers with its status, exit code,
  and short output tails. The full stdout and stderr stay one call away — for
  the failures where they matter.
- **Long runs stay out of the way.** Anything slow moves to the background with
  a run ID the agent can follow, wait on, or stop.
- **Genuinely ad-hoc commands still run.** A plain shell command is only for
  work outside both the discovered tasks and any task surface withheld by a
  runner mode.

The agent gets the usage rules from the server itself, so there is nothing here
you have to teach it.

## Documentation

[`docs/`](docs/README.md) is the documentation index. Start with the
[agent guide](docs/agent-guide.md): the object model, discovery rules, task IDs
per runner, the run lifecycle, and the MCP tool reference.

## Security

Tasks and shell commands run with your privileges and without a sandbox: trust
a selected task the way you trust the project's build scripts. The only
server-side authorization mechanism is runner permission declarations and
modes. Runner modes reduce the task surface but do not isolate it.
`run_shell_command` and `start_shell_command` are unrestricted escape hatches
that bypass that mechanism; a task withheld by a runner mode must not be
recreated through them or another shell path. Need isolation? Run
`just-mcp-work` in a container. See [SECURITY.md](SECURITY.md).

## Install

Download an archive from the [latest GitHub Release][latest-release]:

| Platform | Download |
| --- | --- |
| Linux x86_64 | [`just-mcp-work_linux_amd64.tar.gz`][linux-amd64-download] |
| Linux arm64 | [`just-mcp-work_linux_arm64.tar.gz`][linux-arm64-download] |
| macOS Apple Silicon | [`just-mcp-work_darwin_arm64.tar.gz`][macos-arm64-download] ([opening notes][macos-notes]) |
| Windows x86_64 | [`just-mcp-work_windows_amd64.zip`][windows-amd64-download] |

Extract it and put `just-mcp-work` (or `just-mcp-work.exe`) on `PATH`. The
release [`checksums.txt`][checksums-download] verifies the archive. From source:

```console
go install github.com/palchukovsky/just-mcp-work/cmd/just-mcp-work@latest
```

The [Go toolchain](https://go.dev/), [`just`](https://just.systems/),
[CMake](https://cmake.org/), [Docker](https://www.docker.com/) with the
[Compose](https://docs.docker.com/compose/) v2 plugin, and
[GNU Make](https://www.gnu.org/software/make/) are needed only for the project
types you actually have. A tool missing on this host is reported as a warning,
and everything else in the workspace keeps working.

### Go runner authorization

A regular `go.mod` exposes synthesized Go tasks according to the selected
runner mode:

| Mode | Surface | Caller arguments |
| --- | --- | --- |
| `safe` (default) | Four fixed tasks | Rejected |
| `all` | Safe plus three tasks | Accepted only by `go:any` |
| `disabled` | No Go tasks | Not applicable |

Safe mode exposes `go:build`, `go:test`, `go:vet`, and `go:mod:download`.
Their argv are fixed as `go build ./...`, `go test ./...`, `go vet ./...`, and
`go mod download`. All mode adds fixed `go:fmt` (`go fmt ./...`) and
`go:mod:tidy` (`go mod tidy`) tasks. It also adds `go:any`, which forwards any
non-empty Go argv exactly as supplied. Disabled mode does not construct the Go
runner or discover or run Go tasks.

`safe` is reduced access, not a sandbox. `go test` executes checkout code, Go
may run toolchains and helper programs, and `go mod download` may use the
network and write the module cache. `all` also permits arbitrary Go argv;
Go exec and tool hooks can launch external programs without going through a
shell.

## Set it up

Run this once in the workspace:

```console
just-mcp-work init
```

Each invocation is authoritative inside the workspace scope resolved from
`--dir`, for the surfaces it manages. It adds the canonical instruction block
for the selected agents — Claude Code, Codex, Cursor, Copilot, and Windsurf —
and never reads or changes the instruction file of an agent that is not
selected. `.claude/settings.json` is touched only when `claude` is one of the
selected agents, and then follows the permission answer. `.mcp.json` and
`.codex/config.toml` follow `--write-mcp-config` rather than `--agents`: they
are rewritten when it is true and stripped of their JMW entries when it is
false, whether or not `codex` was selected. Two agent targets that resolve to
one document — a `CLAUDE.md` symlinked to an `AGENTS.md`, say — are each
written with their own header, so keep such a document under a single selected
agent, or give it text of its own before the first `init`.

A file left holding nothing but JMW state is removed, which can happen to
`.mcp.json`, `.codex/config.toml`, and `.claude/settings.json`, except that a
JMW-only `.mcp.json` scope anchor is kept as an empty object whenever deleting
it would change the scope of an identical repeated `init`. Instruction files
are never removed: an agent dropped from `--agents` keeps the block an earlier
run wrote for it and goes on obeying it, so delete that block by hand once the
agent should stop.

Every target this invocation manages is planned before any of them is written,
so a failure on a later target cannot leave an earlier one already changed.
Existing foreign entries and text are edited in place without broad
reformatting: their content, ordering, formatting, and line endings are
preserved. When removing an appended managed block, `init` also removes its
separator from a newline-terminated foreign file; a legacy file that had no
final newline is kept as valid text with one final line break because the
previous state is no longer distinguishable. `init` stops when it cannot edit a
target safely or finds a hand-written Codex entry for this server; it tells you
what to fix instead of taking the entry over. It never searches for instruction
or agent configuration targets above the resolved workspace scope.

Every runner must register a permission declaration before it can enter the
runtime catalog. `init` asks about every declared runner; Go defaults to
`safe`. Just, Make, CMake, and Docker are currently unreviewed and offer their
existing `all` behavior by default or `disabled` for compatibility while their
command surfaces are reviewed separately. Pass the repeatable
`init --runner-mode <name>=<mode>` option to answer selected runner questions
non-interactively. `init` persists the complete canonical selection in managed
MCP and Codex server arguments, and those arguments drive `serve`. Manual
`serve` invocations support the same repeatable `--runner-mode` option.

Run `init` again after an update. `init --help` and `serve --help` list the
agent targets and the server options.

## Configuration

| Flag | Environment | Default |
| --- | --- | --- |
| `--root` | `JMW_ROOT` | Current directory |
| `--timeout` | `JMW_TIMEOUT` | `15m` (`0` disables the timeout) |
| `--sync-deadline` | `JMW_SYNC_DEADLINE` | `1m` |
| `--retention` | `JMW_RETENTION` | `72h` |
| `--exclude` | — | None |
| `--runner-mode <name>=<mode>` | — | Each runner's declared default |

Run data is kept under `.just-mcp-work/log/` in the selected workspace.

## Development and release

With [Just](https://just.systems/):

```console
just install-lint
just verify
just build-all
just package
```

`just verify` checks formatting, dependencies, strict lint, vet, race-enabled
tests, build, and the MCP smoke flow. `just build-all` produces the Linux,
macOS, and Windows binaries; `just package` creates the release archives and
checksums.

`just release patch|minor|major` verifies the project, creates and pushes the
next tag, then GitHub Actions builds and publishes the release.
`just release-dry [patch|minor|major]` runs the same checks and starts a
pipeline dry run without creating a tag; it needs authenticated GitHub CLI
access.

## License

[MIT](LICENSE)

[verify-badge]: https://github.com/palchukovsky/just-mcp-work/actions/workflows/ci.yml/badge.svg
[verify-workflow]: https://github.com/palchukovsky/just-mcp-work/actions/workflows/ci.yml
[release-badge]: https://github.com/palchukovsky/just-mcp-work/actions/workflows/release.yml/badge.svg
[release-workflow]: https://github.com/palchukovsky/just-mcp-work/actions/workflows/release.yml
[release-version-badge]: https://img.shields.io/github/v/release/palchukovsky/just-mcp-work
[releases]: https://github.com/palchukovsky/just-mcp-work/releases
[latest-release]: https://github.com/palchukovsky/just-mcp-work/releases/latest
[linux-amd64-download]: https://github.com/palchukovsky/just-mcp-work/releases/latest/download/just-mcp-work_linux_amd64.tar.gz
[linux-arm64-download]: https://github.com/palchukovsky/just-mcp-work/releases/latest/download/just-mcp-work_linux_arm64.tar.gz
[macos-arm64-download]: https://github.com/palchukovsky/just-mcp-work/releases/latest/download/just-mcp-work_darwin_arm64.tar.gz
[windows-amd64-download]: https://github.com/palchukovsky/just-mcp-work/releases/latest/download/just-mcp-work_windows_amd64.zip
[checksums-download]: https://github.com/palchukovsky/just-mcp-work/releases/latest/download/checksums.txt
[macos-notes]: docs/macos.md
[go-badge]: https://img.shields.io/github/go-mod/go-version/palchukovsky/just-mcp-work
[go-cache]: https://pkg.go.dev/github.com/palchukovsky/just-mcp-work
[license-badge]: https://img.shields.io/github/license/palchukovsky/just-mcp-work
[license]: LICENSE
