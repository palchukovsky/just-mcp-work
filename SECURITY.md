# Security

`just-mcp-work` (jmw) is an MCP facade over workspace task runners. This
document describes what it does and does not protect against, so you can
decide how much to trust it in a given setup.

## What jmw executes

jmw runs **tasks that already exist in a project** - a `just` recipe today,
a `make` target or `cmake` build tomorrow — addressed as `<runner>:<task>`
(e.g. `just:build`). It discovers projects under a root and invokes their
existing tasks through the real runner binary.

It does **not** accept arbitrary shell from the agent. The MCP surface lets a
caller run named tasks that are already in the repo; it adds no new "execute
anything" primitive. The agent's blast radius is bounded by what the
project's task files already contain.

## What jmw does NOT do

jmw does **not** sandbox execution. A task, once invoked, runs as a child
process with the same privileges, filesystem access, and environment as the
jmw process itself. It can read and write anywhere that process can, open
network connections, and spawn further processes — whatever the task
definition tells it to.

A task file (justfile, Makefile, …) is code. Pointing jmw at a project is the
same act as running that project's build scripts by hand — because it is the
same thing. Do not point jmw at task files you do not trust.

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
