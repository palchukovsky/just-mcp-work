# just-mcp-work

[![Verify][verify-badge]][verify-workflow]
[![Release][release-badge]][release-workflow]
[![Latest release][release-version-badge]][releases]
[![Go][go-badge]][go-cache]
[![License: MIT][license-badge]][license]

> Just work with your workspace - over MCP.

Your coding agent spends context reading build files and re-reading logs it
already ran. `just-mcp-work` is a small local MCP server that gives it one way
to find and run everything a workspace can run, and answers with a short receipt
instead of a wall of output.

- **No build files in context.** Go modules, Just, CMake, Docker, GNU Make, and
  Git repositories for fixed CLI coding-agent tasks nested anywhere in the
  workspace are discovered on demand. The agent asks for one task and gets that
  one task.
- **Output only when it is wanted.** A run answers with its status, exit code,
  and short output tails. The full stdout and stderr stay one call away - for
  the failures where they matter.
- **Long runs stay out of the way.** Anything slow moves to the background with
  a run ID the agent can follow, wait on, or stop.
- **Genuinely ad-hoc commands still run.** A plain shell command is only for
  work outside both the discovered tasks and any task surface withheld by a
  runner mode.
- **Writes can be bounded per launch.** On macOS, a caller can declare the
  paths a run may write; JMW refuses a scoped launch where it cannot enforce
  that boundary.

The agent gets the usage rules from the server itself, so there is nothing here
you have to teach it.

Tasks and shell commands otherwise run with your privileges and unrestricted
filesystem access: runner modes reduce the task surface but do not isolate it,
and the shell tools bypass them entirely. Read [SECURITY.md](SECURITY.md)
before pointing this at a workspace.

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
[Compose](https://docs.docker.com/compose/) v2 plugin,
[GNU Make](https://www.gnu.org/software/make/),
[`codex`](https://openai.com/codex/), and
[`claude`](https://docs.anthropic.com/en/docs/claude-code/overview) are needed
only for the project types you actually have. A tool missing on this host is
reported as a warning, and everything else in the workspace keeps working.

## Set it up

Run this once in the workspace:

```console
just-mcp-work init
```

It asks whether the workspace takes part in the JMW beta test, where the
managed instruction block goes, what mode each runner gets, which AI families
to declare, and whether the shell tools may run without a client confirmation.
Then it writes those surfaces, the runner policy, and the generated agent
guide. A terminal that can draw it shows the questions as one keyboard-driven
form; anywhere else they come one at a time as text. Every question has a flag
for a scripted run; `init --help` lists them.

[Set up a workspace](docs/setup.md) covers the questions, the files `init`
owns, and when a later release asks you to run it again.

Your MCP client starts `serve` from the configuration `init` wrote:

| Flag | Environment | Default |
| --- | --- | --- |
| `--root` | `JMW_ROOT` | Current directory |
| `--ai` | - | None |
| `--timeout` | `JMW_TIMEOUT` | `15m` (`0` disables the timeout) |
| `--sync-deadline` | `JMW_SYNC_DEADLINE` | `1m` |
| `--retention` | `JMW_RETENTION` | `72h` |
| `--exclude` | - | None |

Run data is kept under `.just-mcp-work/log/` in the selected workspace.

## Build and release

With [Just](https://just.systems/):

```console
just setup
just verify
just build-all
just package
```

`just setup` installs the pinned verification tools into ignored directories in
this checkout: golangci-lint under `.tmp/bin/` and Semgrep under `.venv/`.
`just verify` checks formatting, dependencies, strict lint, vet, race-enabled
tests, build, and the MCP smoke flow. `just build-all` produces the Linux,
macOS, and Windows binaries; `just package` creates the release archives and
checksums. `just build-run <arguments>` builds the command for this machine
and runs it, for example `just build-run init --dir ~/work/my-project`.

`just release patch|minor|major` verifies the project, creates and pushes the
next tag, then GitHub Actions builds and publishes the release.
`just release-dry [patch|minor|major]` runs the same checks and starts a
pipeline dry run without creating a tag; it needs authenticated GitHub CLI
access.

## Documentation

[`docs/`](docs/README.md) is the documentation index. Start with the
[agent guide](docs/agent-guide.md): the object model, discovery rules, task IDs
per runner, the run lifecycle, and the MCP tool reference.

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
