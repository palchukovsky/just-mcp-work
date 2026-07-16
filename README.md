# just-mcp-work

> Just work with your workspace — over MCP.

`just-mcp-work` is a local STDIO MCP server for workspace tasks. It saves
agent context: the agent discovers projects and asks for the selected task
metadata instead of loading justfiles and unrelated workspace details.

## Install

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

[`just`](https://github.com/casey/just) must be available on `PATH` for Just
projects. [CMake](https://cmake.org/) must be available for CMake projects.

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
| `get_run` | Read stored run metadata. |
| `get_run_logs` | Page stdout or stderr by byte offset. |
| `version_status` | Compare this binary with the latest stable GitHub tag. |

Task IDs are runner-qualified, for example `just:build` or
`cmake:build:debug`. `run_task` returns a short receipt; read output separately
with `get_run_logs`.

## Configuration

| Flag | Environment | Default |
| --- | --- | --- |
| `--root` | `JMW_ROOT` | Current directory |
| `--timeout` | `JMW_TIMEOUT` | `15m` |
| `--retention` | `JMW_RETENTION` | `72h` |
| `--exclude` | — | None |

Run data is kept under `.just-mcp-work/log/` in the selected workspace.

## Development and release

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
