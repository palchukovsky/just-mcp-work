# just-mcp-work

[![Verify][verify-badge]][verify-workflow]
[![Release][release-badge]][release-workflow]
[![Latest release][release-version-badge]][releases]
[![Go][go-badge]][go-cache]
[![License: MIT][license-badge]][license]

> Just work with your workspace — over MCP.

**Stop burning agent context on build files.** `just-mcp-work` is a local
STDIO MCP server: one interface to everything runnable in a workspace, handing
the agent only the part it asked for.

- **Run it.** The task, or a plain shell command when no task fits.
- **Browse instead of reading.** [Just](https://just.systems/),
  [CMake](https://cmake.org/), [Docker](https://www.docker.com/), and
  [GNU Make](https://www.gnu.org/software/make/) projects, nested anywhere in
  the workspace — no build file is ever loaded into context.
- **One task at a time.** Full metadata and parameters for the task the agent
  picked, and nothing around it.
- **Output on demand.** A run answers with a short receipt; the full stdout
  and stderr stay one call away, whenever the agent actually needs them.

## Security

`run_task` executes an existing `just` recipe, CMake target, Docker task, or
Make target with arguments kept separate. `run_shell_command` intentionally
passes command text to the operating system shell. Both run with your privileges
and without a sandbox: trust the selected task or command like you trust a
project's build scripts. Need isolation? Run `just-mcp-work` in a container. See
[SECURITY.md](SECURITY.md).

## Install

Must be available on `PATH`:

- [`just`](https://just.systems/) for [Just](https://just.systems/) projects.
- [CMake](https://cmake.org/) must be available for [CMake](https://cmake.org/)
  projects.
- [Docker](https://www.docker.com/) with the
  [Compose](https://docs.docker.com/compose/) v2 plugin must be available for
  Docker projects.
- [GNU Make](https://www.gnu.org/software/make/) must be available for
  [Make](https://www.gnu.org/software/make/) projects.

A tool that is missing on this host keeps its project usable: the runner is
reported as a project warning instead of an error, and the runners that do work
keep their tasks. Docker without the Compose v2 plugin is reported the same way,
and its `Dockerfile` build stays runnable.

### Prebuilt binaries

Download an archive from the [latest GitHub Release][latest-release]:

| Platform | Download |
| --- | --- |
| Linux x86_64 | [`just-mcp-work_linux_amd64.tar.gz`][linux-amd64-download] |
| Linux arm64 | [`just-mcp-work_linux_arm64.tar.gz`][linux-arm64-download] |
| macOS Apple Silicon | [`just-mcp-work_darwin_arm64.tar.gz`][macos-arm64-download] ([opening notes][macos-notes]) |
| Windows x86_64 | [`just-mcp-work_windows_amd64.zip`][windows-amd64-download] |

Extract the matching archive and place `just-mcp-work` (or
`just-mcp-work.exe`) on `PATH`. Verify the archive with the release
[`checksums.txt`][checksums-download] when needed.

### Sources

```console
go install github.com/palchukovsky/just-mcp-work/cmd/just-mcp-work@latest
just-mcp-work version
```

## Use with MCP

Run this once in the workspace to add instructions for supported coding agents.
It finds the closest selected agent instruction and `.mcp.json` in the
workspace or its parent directories. The managed instruction block and
`just-mcp-work` MCP entry are replaced in full, leaving unrelated content and
servers alone. If none exists, it creates the files in the workspace. The MCP
entry uses the absolute path of the running executable, so the client does not
depend on `PATH`:

```console
just-mcp-work init
```

Run `init` again after updating `just-mcp-work` to a new version so the
managed agent instructions and MCP entry are refreshed.

`init` does not overwrite a manually configured Codex
`[mcp_servers.just-mcp-work]` table. Remove that table before running `init` so
the generated managed block can take ownership of the entry.

Pass `--write-mcp-config=false` to print the resolved server entry instead of
writing it.

The server discovers nested projects on demand. Use `init --help` and
`serve --help` for agent targets and server options.

## MCP tools

| Tool | Purpose |
| --- | --- |
| `list_projects` | List filtered projects, runner errors, and warnings. |
| `list_tasks` | List tasks, parameters, statistics, and runner issues. |
| `run_task` | Run a task, promoting a long run to the background. |
| `start_task` | Start a selected task asynchronously. |
| `run_shell_command` | Run a shell command; long runs go to background. |
| `start_shell_command` | Start a shell command asynchronously. |
| `get_run` | Read stored run metadata. |
| `get_run_logs` | Page stdout or stderr by byte offset. |
| `get_run_status` | Read run liveness, output, and duration statistics. |
| `wait_run` | Wait for completion without killing a run on a wait timeout. |
| `stop_run` | Stop a running task owned by this server. |
| `list_runs` | List recent runs, newest first, with the scanned count. |
| `version_status` | Compare this binary with the latest stable GitHub tag. |

`list_projects` defaults to the workspace root at depth 1 and skips dot-directories.
Use `path` to choose a subtree, `max_depth` (`-1` is unlimited) or
`include_hidden` to widen directory coverage, and `runners` to restrict projects.
`applied_filter.pruned.depth` and `.hidden` count skipped directory subtrees;
`.runner_mismatch` counts inspected projects removed by `runners`. `excluded`
reports directories skipped by the operator's policy and cannot be widened. This
filtering applies only to the list: `list_tasks` and task tools can still address
any discovered workspace project.

A project reports `errors` when a runner could not be read and `warnings` when a
runner could not be used without the project being at fault, such as a build tool
that is missing on this host. Only `errors` set the project status to `"error"`,
and a runner that fails halfway still contributes the tasks it did discover.
`list_tasks` repeats the `errors` and `warnings` of the runners it listed, so an
empty listing carries its own explanation.

Task IDs are runner-qualified across the `just`, `cmake`, `docker`, and `make`
runners, for example `just:build`, `cmake:build:debug`, `docker:compose:up`, or
`make:test`. Make projects expose their explicit targets without running recipes
during task discovery. For a short task, use `run_task`; it waits for up to
`max_wait_ms`, `--sync-deadline`, or `JMW_SYNC_DEADLINE` (in that precedence
order). A receipt with `status: "running"`, `completed: false`, and
`promoted: true` is successful handoff to a background run, not a failure: use
its `run_id` with `wait_run` or `get_run_status` and do not run the task again.
Use `start_task` up front for a task with a long historical average.

When an MCP client supplies a progress token, synchronous task calls publish the
`run_id` immediately and report elapsed time, output age, and byte counts every
10 seconds. `get_run_status`, `wait_run`, and `list_tasks` expose duration
statistics derived from retained run metadata; aborted runs are counted but do
not affect duration averages. `last_output_age_ms` is the anti-hang signal.

`list_runs` returns a bounded page of newest ledger entries. It reports
`scanned` and, when `truncated` is true, a `next_cursor` for the next page, so
filters cannot silently hide matching older runs.

`run_shell_command` is separate from project discovery: it accepts command
text and an optional workspace-relative `working_directory` (default `.`), so
it can run from directories that have no Just, CMake, Docker, or Make project.
It uses the current OS shell (`$SHELL`, falling back to `/bin/sh`, on Unix;
`ComSpec` on Windows). The working directory must exist inside the workspace and
cannot be a symlink.

`get_run_status` is non-blocking. `wait_run` waits up to `max_wait_ms` (30s by
default); set it to `0` for an immediate snapshot. Status tools return 4096
tail bytes by default; set `tail_bytes` to `0` when output is not needed.

Each task has a 15-minute timeout by default. Set `--timeout=0` or
`JMW_TIMEOUT=0` to disable that timeout entirely; no task timer is created in
that mode. The selected timeout is recorded with the run, so a later server
configuration cannot misreport a foreign run's deadline.

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
tests, build, and the MCP smoke flow. `just build-all` produces Linux, macOS,
and Windows binaries; `just package` creates the release archives and checksums.

`just release patch|minor|major` verifies the project, creates and pushes the
next tag, then GitHub Actions builds and publishes the release. Use
`just release-dry [patch|minor|major]` to run the same checks and start a
pipeline dry run without creating a tag. The dry run needs authenticated
GitHub CLI access.

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
