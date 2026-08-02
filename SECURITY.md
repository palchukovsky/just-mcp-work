# Security

`just-mcp-work` (jmw) is an MCP facade over workspace task runners. This
document describes what it does and does not protect against, so you can
decide how much to trust it in a given setup.

## What jmw executes

jmw runs **tasks that already exist in a project** - a `just` recipe, a `make`
target, a `cmake` preset or configured build target, or a Docker build and
Compose service — addressed as `<runner>:<task>` (e.g. `just:build`). It
discovers projects under a root and invokes their existing tasks through the
real runner binary.

The `run_shell_command` and `start_shell_command` tools are explicit escape
hatches for commands that no discovered task covers. They pass caller-provided
command text to the operating system shell, so their blast radius is not bounded
by project task definitions. Grant access to them only when arbitrary shell
execution is acceptable.

CMake target discovery reads an existing `CMakeCache.txt` and `build.ninja`;
listing does not configure or regenerate the build tree. Treat generated build
trees as executable metadata: a selected target is passed to `cmake --build`
with the same privileges as every other task.

## What jmw does NOT do

jmw does **not** sandbox execution. A task or shell command, once invoked, runs
as a child process with the same privileges, filesystem access, and environment
as the jmw process itself. It can read and write anywhere that process can,
open network connections, and spawn further processes — whatever the task
definition or command text tells it to.

A task file (justfile, Makefile, Dockerfile, Compose manifest, …) is code.
Pointing jmw at a project is the same act as running that project's build
scripts by hand — because it is the same thing. Do not point jmw at task files
you do not trust.

Docker tasks reach the furthest. A build executes the instructions of the
project `Dockerfile`, and a Compose service runs with the bind mounts,
published ports, and privileges its manifest declares — all through the Docker
daemon, which is a privileged service on most hosts. Compose services are
started detached, so their containers outlive the run that started them until
`docker:compose:down` stops them.

## Lifecycle controls

These bound *runaway* processes, not what a process is allowed to do:

- Every run has a timeout.
- On timeout or cancellation, jmw terminates the whole child process tree.
- Termination is best-effort: a task that deliberately daemonizes (`setsid`,
  `nohup`, double-fork) can detach and survive reaping.

`stdout`/`stderr` are persisted to disk under the workspace and subject to
retention. Treat those logs as you would any build output that may contain
secrets a task echoed.

## If you need real isolation

Run jmw inside a container, devcontainer, or VM. jmw is designed to compose
with that boundary — it relies on the surrounding environment for containment
rather than trying to be a sandbox itself. If a task must not touch your
host, put jmw somewhere that task cannot reach the host.

## Reporting a vulnerability

Please report suspected vulnerabilities privately via GitHub's private
vulnerability reporting on this repository (Security → Report a
vulnerability), rather than opening a public issue. Swap this for your own
contact channel if you prefer one.
