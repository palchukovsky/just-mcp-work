# Security

`just-mcp-work` (jmw) is an MCP facade over workspace task runners. This
document describes what it does and does not protect against, so you can
decide how much to trust it in a given setup.

## What jmw executes

jmw runs tasks addressed as `<runner>:<task>` (for example, `just:build`). Just,
Make, CMake, and Docker tasks come from project recipes, targets, presets,
Dockerfiles, and Compose manifests. Go tasks are synthesized by jmw from a
fixed command table when it finds a regular `go.mod`. Agent tasks launch a CLI
coding agent, which then runs unsandboxed, with the operator's own permissions,
in the checkout. jmw fixes the command shape and argument surface apart from
the caller-supplied prompt that becomes the agent's instructions; it does not
constrain what the launched agent then does.

## Runner authorization

Every runner must register a permission declaration before it can enter the
runtime catalog. `init` asks for a mode from each declaration and writes the
complete selection to `.just-mcp-work.json` in the workspace scope root, next
to `.mcp.json`. Managed MCP and Codex server arguments are `serve --root <dir>`
and carry no runner selection. A repeatable `init --runner-mode <name>=<mode>`
option answers selected questions non-interactively. `serve --runner-mode` is
retired and tells the operator to run `init`.

For automation, pass `--runner-mode <name>=<mode>` for every runner whose
question is not answered interactively. If input ends with a runner question
unanswered, `init` fails and names that runner and flag instead of accepting a
mode. When an existing policy is readable, an interactive prompt offers its
current mode and labels it `current`; otherwise it offers the declared default.
If the existing policy cannot be parsed or has an unsupported current mode,
`init` prints that fallback. If the registered runner set changed, it prints
that it keeps matching current modes, uses declared defaults for new runners,
and drops unregistered runners. A successful `init` invocation makes the
complete policy authoritative.

The policy is fail-closed. A policy that omits a registered runner is rejected
at startup and names the missing runners; a truncated or hand-edited policy
cannot inherit a default. An absent `.just-mcp-work.json` disables every runner,
so no task is discovered or run; the shell tools are unaffected and remain the
escape hatch described below. `serve` warns the operator to run `init`. This
includes a fresh workspace where `init` has never run: jmw exposes no tasks
until the policy exists. Deleting the policy therefore cannot widen the task
surface.

The reviewed declarations provide these modes:

### Go

| Mode | Surface | Caller arguments |
| --- | --- | --- |
| `safe` (default) | Four fixed commands | Rejected |
| `all` | Safe plus three commands | Accepted only by `go:any` |
| `disabled` | No Go commands | Not applicable |

Safe exposes fixed `go build ./...`, `go test ./...`, `go vet ./...`, and
`go mod download`. All adds fixed `go fmt ./...`, fixed `go mod tidy`, and
`go:any`, which forwards any non-empty Go argv exactly. Disabled does not
construct the Go runner, so it discovers and runs nothing.

Safe mode reduces the exposed command surface; it does not create an isolation
boundary. Tests execute code from the checkout, Go may invoke toolchains and
helper programs, and module download may access the network and write the
module cache. All mode also permits arbitrary Go argv. Go exec and tool hooks
can launch external programs without using a shell.

### Agent

The agent declaration has `safe` (default) and `disabled` modes; there is no
`all` mode.

| Mode | Surface | Caller arguments |
| --- | --- | --- |
| `safe` (default) | Fixed tasks | Prompt/model accepted; effort restricted |
| `disabled` | No agent tasks | Not applicable |

Safe exposes only the fixed `agent:codex` and `agent:claude` tasks. The caller
supplies the non-blank `prompt`, arbitrary text that becomes the launched
agent's instructions and is placed after literal `--`; a flag-shaped prompt is
accepted. `model` is free-form, but may not have surrounding whitespace or
start with `-`; only `effort` is restricted to a fixed vocabulary. The binary
and flag skeleton are fixed, but this is not an isolation boundary: the launched
agent inherits the operator's permissions in the checkout. Disabled does not
construct the agent runner, so it discovers and runs nothing.

### Unreviewed runners

Just, Make, CMake, and Docker are currently explicit unreviewed declarations.
For compatibility they offer their existing unrestricted `all` behavior by
default or `disabled`; their command review is tracked separately.

### Shell escape hatch

The `run_shell_command` and `start_shell_command` tools pass caller-provided
command text to the operating system shell. They remain available for genuinely
ad-hoc commands outside the discovered or withheld task surfaces. A task may be
absent because its runner mode withheld it; agents must not recreate or run that
task through either shell tool or another shell path. Runner selections do not
implement a general shell authorization policy, so grant access to the shell
tools only when arbitrary shell execution is acceptable.

### CMake

CMake target discovery reads an existing `CMakeCache.txt` and `build.ninja`;
listing does not configure or regenerate the build tree. Treat generated build
trees as executable metadata: a selected target is passed to `cmake --build`
with the same privileges as every other task.

## The safe Make subset

Make target discovery reads the project `GNUmakefile`, `Makefile`, or
`makefile` as text. It never asks Make to evaluate it, so nothing in the build
file runs in order to find out what could be run. That is a deliberate
boundary, not a missing feature: evaluating a Makefile to enumerate its targets
would execute build-file logic during discovery, which is a different trust
model from executing it only when a task is invoked.

The subset lists literal explicit targets, including the ones a literal
`.PHONY` rule names. Consequently:

- Pattern rules, targets whose names begin with a dot, and the Makefile itself
  are never listed.
- A construct that cannot be read literally - `include`, conditionals,
  `define`, `$(eval)`, a custom `.RECIPEPREFIX`, or a target assembled from
  variables, functions, or wildcards - is reported as a Make discovery error
  for that project rather than as a silently shortened target list. The other
  runners of the same project keep working.

The listed set is therefore not a promise of completeness, and a Make project
whose build file leaves the subset lists nothing at all. There is no evaluated
discovery mode. If one is ever added it must be opt-in and labelled as the
different trust model it is.

A target discovery cannot see is not a target an authorization decision
withheld: no runner mode is involved, and the rule against recreating a
withheld task through a shell does not apply to it. Run it the way you run any
other command that has no task - through the shell tools or your own terminal -
and trust it exactly as much as you trust the rest of that Makefile.

## What jmw does NOT do

jmw does **not** sandbox execution. No runner mode provides isolation. A task or
shell command, once invoked, runs as a child process with the same privileges,
filesystem access, and environment as the jmw process itself. It can read and
write anywhere that process can, open network connections, and spawn further
processes - whatever the task definition, synthesized command, or command text
tells it to.

A task file (justfile, Makefile, Dockerfile, Compose manifest, …) is code.
Pointing jmw at a project is the same act as running that project's build
scripts by hand - because it is the same thing. Do not point jmw at task files
you do not trust.

Docker tasks reach the furthest. A build executes the instructions of the
project `Dockerfile`, and a Compose service runs with the bind mounts,
published ports, and privileges its manifest declares - all through the Docker
daemon, which is a privileged service on most hosts. Compose services are
started detached, so their containers outlive the run that started them until
`docker:compose:down` stops them.

Permission and approval rules configured in the calling client - agent
allow-lists, approval modes, and per-tool confirmation prompts - are
convenience and operator discipline, not a server-side security boundary. jmw
executes what it is handed; it neither knows nor relies on what the calling
agent chose to confirm or auto-approve.
[Anthropic](https://www.anthropic.com/engineering/claude-code-auto-mode)
reports that Claude Code users approve 93% of permission prompts and names the
effect *approval fatigue*. A confirmation prompt that a human approves nine
times out of ten is not a control you should rely on.

Runner permission declarations and modes are jmw's server-side authorization
mechanism. The explicit shell tools are an escape hatch: when enabled,
`run_shell_command` and `start_shell_command` give the caller a general shell
outside runner task filtering.

## Lifecycle controls

Lifecycle controls are separate from runner authorization. They bound
*runaway* processes, not what a process is allowed to do:

- Every run has a timeout.
- On timeout or cancellation, jmw terminates the whole child process tree.
- Termination is best-effort: a task that deliberately daemonizes (`setsid`,
  `nohup`, double-fork) can detach and survive reaping.

`stdout`/`stderr` are persisted to disk under the workspace and subject to
retention. Treat those logs as you would any build output that may contain
secrets a task echoed.

## If you need real isolation

Run jmw inside a container, devcontainer, or VM. jmw is designed to compose
with that boundary - it relies on the surrounding environment for containment
rather than trying to be a sandbox itself. If a task must not touch your
host, put jmw somewhere that task cannot reach the host.

## Reporting a vulnerability

Please report suspected vulnerabilities privately via GitHub's private
vulnerability reporting on this repository (Security → Report a
vulnerability), rather than opening a public issue. Swap this for your own
contact channel if you prefer one.
