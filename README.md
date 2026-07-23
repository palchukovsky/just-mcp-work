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

- **No build files in context.** Just, CMake, Docker, and GNU Make projects,
  nested anywhere in the workspace, are discovered on demand. The agent asks for
  one task and gets that one task.
- **Output only when it is wanted.** A run answers with its status, exit code,
  and short output tails. The full stdout and stderr stay one call away — for
  the failures where they matter.
- **Long runs stay out of the way.** Anything slow moves to the background with
  a run ID the agent can follow, wait on, or stop.
- **Anything else still runs.** A plain shell command, when no task fits.

The agent gets the usage rules from the server itself, so there is nothing here
you have to teach it.

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

[`just`](https://just.systems/), [CMake](https://cmake.org/),
[Docker](https://www.docker.com/) with the
[Compose](https://docs.docker.com/compose/) v2 plugin, and
[GNU Make](https://www.gnu.org/software/make/) are needed only for the project
types you actually have. A tool missing on this host is reported as a warning,
and everything else in the workspace keeps working.

## Set it up

Run this once in the workspace:

```console
just-mcp-work init
```

It registers the server and adds a short instruction block for the coding agents
you use — Claude Code, Codex, Cursor, Copilot, Windsurf. Existing configuration
is edited in place: only the entries this server owns are rewritten, and your
own content, ordering, and formatting are left untouched, down to the line
endings. `init` stops when it cannot edit a configuration safely or finds a
hand-written Codex entry for this server; it tells you what to fix instead of
taking the entry over. For Claude Code it also offers matching tool permissions
and asks before changing them.

Run `init` again after an update. `init --help` and `serve --help` list the
agent targets and the server options.

## Security

Tasks and shell commands run with your privileges and without a sandbox: trust a
selected task the way you trust the project's build scripts. `run_task` runs an
existing recipe or target with its arguments kept separate; `run_shell_command`
deliberately hands command text to the OS shell. Need isolation? Run
`just-mcp-work` in a container. See [SECURITY.md](SECURITY.md).

## Configuration

| Flag | Environment | Default |
| --- | --- | --- |
| `--root` | `JMW_ROOT` | Current directory |
| `--timeout` | `JMW_TIMEOUT` | `15m` (`0` disables the timeout) |
| `--sync-deadline` | `JMW_SYNC_DEADLINE` | `1m` |
| `--retention` | `JMW_RETENTION` | `72h` |
| `--exclude` | — | None |

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
