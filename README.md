# coder

A small coding agent that runs offline: a local model, a tool-calling
loop over a project directory, and a web UI. On Windows it is a desktop
app whose agent runs inside WSL.

Two binaries from one module:

- **`coder`** (Linux, runs in WSL): the daemon. The shuttle UI, the agent
  loop, the tools (jailed to a project directory), and sessions as
  append-only JSONL files under `~/.coder/sessions`. It talks to any
  OpenAI-compatible server: colibri's `coli serve`, LM Studio, Ollama.
- **`coder-shell.exe`** (Windows, cross-built from WSL, no cgo): a
  WebView2 window that loads the KAT coder model in-process through
  [colibri-go](https://github.com/pietjan/colibri-go), serves it on
  localhost, starts `coder serve` in WSL and shows its UI.

The model starts each session with one tool, `tools`, which lists the
rest and hands out their schemas on request; what it describes stays in
the request for the rest of the session. Read-only tools run on their
own; writes, deletes and commands wait for a click in the UI, with an
"allow for this session" option per tool.

## Building

```bash
make work           # once: go.work over the sibling checkouts (loom, shuttle, spectacle, colibri-go)
make build          # templ + css + bin/coder
make build/windows  # coder-shell.exe, staged under %TEMP%\coder on the Windows side
make test
make audit
```

`tailwindcss` must be on PATH for `make css`; the compiled sheet is
committed, so a fresh clone builds before it has ever run it. Without
`go.work` the pins in `go.mod` are used instead (`GOWORK=off`).

## Running

In a browser, against any model server:

```bash
bin/coder serve --listen 127.0.0.1:8090 --llm http://127.0.0.1:8080/v1 --project ~/src/thing
```

Then open <http://127.0.0.1:8090/>. `--model` picks a model id; empty
takes the first one the server lists. Without a real model:

```bash
go run ./internal/testopenai/cmd/fakellm      # scripted model on :8099
bin/coder serve --llm http://127.0.0.1:8099/v1
```

One turn from the terminal (approvals are printed and denied; `--yes`
allows them):

```bash
bin/coder ask --llm http://127.0.0.1:8080/v1 --project . "what does this repo do?"
```

On Windows, run `coder-shell.exe`. The first run writes
`%APPDATA%\coder\config.json` with defaults; edit it, or pass the same
settings as flags (`-model`, `-budget`, `-vram`, `-project`, ...). The
log is `%LOCALAPPDATA%\coder\shell.log`. `budget` and `vram` are
required: the shell does not size them automatically (that needs
colibri's internal Vulkan package). WSL must run in mirrored networking
mode (`networkingMode=mirrored` in `.wslconfig`) so the daemon reaches
the model on localhost and the window reaches the daemon.

## Layout

```
cmd/coder          the daemon: serve, ask
cmd/coder-shell    the Windows app: config, engine (the coli serve sequence), launcher, loading page
internal/llm       a small OpenAI chat-completions client (streaming, tool calls, reasoning_content)
internal/tool      the registry, the tools meta-tool, the project tools, the path jail
internal/agent     the turn loop, the approval gate, the event stream, history rebuild
internal/session   JSONL session files
internal/hub       live sessions: persistence and fan-out to the UI
internal/ui        the shuttle component and its templ views
internal/testopenai  a scripted model for tests and for driving the UI by hand
assets             the stylesheet, the vendored Datastar bundle, the icon
```

`internal/tool`'s file operations, shell runner and path jail are copied
from [toto](https://github.com/pietjan/toto); the turn loop is adapted
from its OpenAI driver.
