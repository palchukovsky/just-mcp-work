# Security

`just-mcp-work` (jmw) is an MCP facade over workspace task runners. This
document describes what it does and does not protect against, so you can
decide how much to trust it in a given setup.

## What jmw executes

jmw runs tasks addressed as `<runner>:<task>` (for example, `just:build`). Just,
Make, CMake, and Docker tasks come from project recipes, targets, presets,
Dockerfiles, and Compose manifests. Go tasks are synthesized by jmw from a
fixed command table when it finds a regular `go.mod`. Agent tasks launch a CLI
coding agent with the operator's own permissions in the checkout. jmw fixes the
command shape and argument surface apart from the caller-supplied prompt that
becomes the agent's instructions. A per-launch `write_scope` can constrain
filesystem writes as described below; without one, jmw does not constrain what
the launched agent then does.

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
agent inherits the operator's permissions in the checkout. A per-launch
`write_scope` can add the write boundary described below independently of the
runner mode. A scoped Codex launch uses `--sandbox danger-full-access` for the
macOS compatibility described below. Disabled does not construct the agent
runner, so it discovers and runs nothing.

### Unreviewed runners

Just, Make, CMake, and Docker are currently explicit unreviewed declarations.
For compatibility they offer their existing unrestricted `all` behavior by
default or `disabled`; their command review is tracked separately.

### Shell escape hatch

The `define_shell_block`, `run_shell_command`, and `start_shell_command` tools
form the shell escape hatch. `define_shell_block` shares the execution tools'
permission group, and a block stores either shell text in `command` or an exact
`argv`, which executes the named program directly, without a shell. Both kinds
fix the working directory at definition.

What the operator sees differs between the two calls. At definition they see the
whole stored block: the shell text, or the argv the block fixes. At run time a
shell-text block presents only its `block_id`, not the text it stands for. An
argv block run instead carries the full argv in `arguments`, which the server
requires to start with the argv the block fixed; the run a client asks for
therefore always names the program it is about to execute, and the block's own
elements cannot be replaced by the run that uses it.

These tools remain available for genuinely ad-hoc commands outside the
discovered or withheld task surfaces. A task may be absent because its runner
mode withheld it; agents must not recreate or run that task through any of these
tools or another shell path. Runner selections do not implement a general shell
authorization policy, so grant access to the shell tools only when arbitrary
shell execution is acceptable.

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

## Write scope

`run_task`, `start_task`, `run_shell_command`, and `start_shell_command` accept
an optional `write_scope`: paths relative to the server's worktree root, the
same root returned as `worktree_root` and used by `project_path` and
`working_directory`. The scope belongs to that launch. It may accompany a
`block_id`; `define_shell_block` does not store one. Omitting it or sending
`null` preserves the unrestricted behavior and adds no receipt or metadata
field.

JMW rejects an empty list, an absolute or blank entry, an entry that escapes
through `..`, one whose existing prefix resolves through a symlink outside the
root, or a declared path whose final component is itself a symbolic link. A
symbolic-link path must be replaced by its target in the declaration. These are
MCP errors before a run is recorded or started. JMW adds the process temporary
directory (`TMPDIR` when set, otherwise `/tmp`) to accepted scopes. For
`agent:codex` it also adds `CODEX_HOME`, or `~/.codex` when unset; for
`agent:claude` it adds `CLAUDE_CONFIG_DIR`, or `~/.claude` when unset. A
temporary or agent state path whose final component is a symbolic link is
refused as `spawn_error`. The scope is also refused if either added path
contains or equals a declared path. The effective list has symlinks resolved
and contained paths folded. It is returned as `write_scope` in the receipt,
persisted in `meta.json`, and is exactly what the OS enforces.

On macOS, JMW starts the run under `/usr/bin/sandbox-exec` with a profile that
denies every file write outside the effective paths, except writes to
`/dev/null`, `/dev/zero`, `/dev/stdout`, `/dev/stderr`, and numbered inherited
descriptors under `/dev/fd/<number>`. The kernel rejects an out-of-scope write
when it is attempted with `Operation not permitted`, and the file is never
created. Every descendant process inherits the restriction, including a
background child that detaches and outlives its parent. Writes through an
in-scope symlink to an outside target and renames from inside to outside are
also rejected.

On every other OS, or when `/usr/bin/sandbox-exec` is missing, a scoped run is
refused with `spawn_error` and never starts. There is no degraded mode.

macOS does not allow a sandboxed process to apply a second sandbox. Codex
normally sandboxes the commands it runs, so `agent:codex` launched with a
`write_scope` receives `--sandbox danger-full-access`. Codex's own sandbox is
then off and JMW's profile is the only write boundary. JMW adds that flag only
together with a successfully established JMW boundary, never for an unscoped
launch. Codex's other default restrictions, including its network restriction,
do not apply to the scoped run. The `agent:claude` command is unchanged. An
agent configured to apply its own macOS sandbox, such as Claude Code with its
sandbox enabled, cannot run under a JMW write scope.

The boundary deliberately does not cover these cases:

- Reads, network access, and process execution remain unrestricted. This is a
  write boundary, not isolation.
- Only the run's process tree is constrained. The calling client, its MCP
  servers, daemons such as Docker, and already-running services are outside the
  boundary. Handing work to one of them can cause writes outside the scope.
- Tool caches outside the effective list are not writable. This includes a Go
  build or module cache. A direct write to `/tmp` is writable when `TMPDIR` is
  unset, but not when `TMPDIR` names a different directory. Build, test, and
  lint gates normally run without `write_scope`.
- A hard link created before the run by a process outside the boundary can give
  an in-scope name to an outside file. The run can then change that file's
  content through the in-scope name. The scoped run cannot create such a link
  itself because linking to the outside file is refused.
- A scoped run starts through SIP-protected `/usr/bin/sandbox-exec`, so macOS
  removes `DYLD_*` variables before the task starts. Shell-tool runs through a
  SIP-protected system shell such as `/bin/sh` or `/bin/zsh` already lose them.
  A directly launched task program loses them only when the run is scoped.
- Git writes repository metadata. In a linked worktree, the main repository's
  common Git directory may be outside the server's worktree root and therefore
  cannot be declared. Git operations that write the index or history then fail
  under a scope.
- The scope is opt-in and per call. It restrains only a caller that requests it,
  such as an orchestrator bounding the executor it launches. A caller can omit
  it, so it is not an authorization policy.

This write boundary is not a substitute for the isolation described in
[If you need real isolation](#if-you-need-real-isolation).

## What jmw does NOT do

No runner mode provides isolation. Without `write_scope`, a task or shell
command runs as a child process with the same privileges, filesystem access,
and environment as the jmw process itself. It can read and write anywhere that
process can, open network connections, and spawn further processes - whatever
the task definition, synthesized command, or command text tells it to.

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
`define_shell_block`, `run_shell_command`, and `start_shell_command` let the
caller carry or execute general shell command text outside runner task filtering.

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
with that boundary. Its optional write scope is not isolation. If a task must
not touch your host, put jmw somewhere that task cannot reach the host.

## Reporting a vulnerability

Please report suspected vulnerabilities privately via GitHub's private
vulnerability reporting on this repository (Security → Report a
vulnerability), rather than opening a public issue. Swap this for your own
contact channel if you prefer one.
