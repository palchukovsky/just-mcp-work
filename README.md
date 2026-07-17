# just-mcp-work

> Just work with your workspace — over MCP.

**Stop burning agent context on build files.** `just-mcp-work` is a local
STDIO MCP server: one interface to everything runnable in a workspace, handing
the agent only the part it asked for.

- **Run it.** The task, or a plain shell command when no task fits.
- **Browse instead of reading.** [Just](https://just.systems/),
  [CMake](https://cmake.org/), and
  [GNU Make](https://www.gnu.org/software/make/) projects, nested anywhere in
  the workspace — no build file is ever loaded into context.
- **One task at a time.** Full metadata and parameters for the task the agent
  picked, and nothing around it.
- **Output on demand.** A run answers with a short receipt; the full stdout
  and stderr stay one call away, whenever the agent actually needs them.

## Install

Must be available on `PATH`:

- [`just`](https://just.systems/) for [Just](https://just.systems/) projects.
- [CMake](https://cmake.org/) must be available for [CMake](https://cmake.org/)
  projects.
- [GNU Make](https://www.gnu.org/software/make/) must be available for
  [Make](https://www.gnu.org/software/make/) projects.

### Prebuilt binaries

Every GitHub Release contains these archives:

| Platform | Archive |
| --- | --- |
| Linux x86_64 | `just-mcp-work_linux_amd64.tar.gz` |
| Linux arm64 | `just-mcp-work_linux_arm64.tar.gz` |
| macOS Apple Silicon | `just-mcp-work_darwin_arm64.tar.gz` |
| Windows x86_64 | `just-mcp-work_windows_amd64.zip` |

Extract the matching archive and place `just-mcp-work` (or
`just-mcp-work.exe`) on `PATH`. Verify the archive with the release
`checksums.txt` when needed.

### Sources

```console
go install github.com/palchukovsky/just-mcp-work@latest
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

Pass `--write-mcp-config=false` to print the resolved server entry instead of
writing it.

The server discovers nested projects on demand. Use `init --help` and
`serve --help` for agent targets and server options.

## MCP tools

| Tool | Purpose |
| --- | --- |
| `list_projects` | List discovered projects and runner errors. |
| `list_tasks` | List tasks and parameters for a project. |
| `run_task` | Start a selected task with separate argument values. |
| `run_shell_command` | Run a shell command inside the workspace. |
| `get_run` | Read stored run metadata. |
| `get_run_logs` | Page stdout or stderr by byte offset. |
| `version_status` | Compare this binary with the latest stable GitHub tag. |

Task IDs are runner-qualified, for example `just:build`, `cmake:build:debug`,
or `make:test`. Make projects expose their explicit targets without running
recipes during task discovery. `run_task` returns a short receipt; read output
separately with `get_run_logs`.

`run_shell_command` is separate from project discovery: it accepts command
text and an optional workspace-relative `working_directory` (default `.`), so
it can run from directories that have no Just, CMake, or Make project. It uses
the current OS shell (`$SHELL`, falling back to `/bin/sh`, on Unix; `ComSpec`
on Windows). The working directory must exist inside the workspace and cannot
be a symlink.

## Configuration

| Flag | Environment | Default |
| --- | --- | --- |
| `--root` | `JMW_ROOT` | Current directory |
| `--timeout` | `JMW_TIMEOUT` | `15m` |
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
