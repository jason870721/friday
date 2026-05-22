# friday

A small ReAct agent built on the [evva](https://github.com/johnny1110/evva)
Go SDK. Persona inspired by Tony Stark's F.R.I.D.A.Y. — concise, witty, gets
on with the job.

Phase 15 of the evva roadmap: a real downstream consumer that exercises
the `pkg/` surface end-to-end (config, profile, tools, LLM registry, event
sink, bubbletea-driven UI). Rough edges discovered along the way are
captured in [docs/sdk-feedback.md](docs/sdk-feedback.md).

## Setup

```sh
# 1. First run — auto-creates ~/.friday/config/friday-config.yml and
#    prints a hint about where to put your API key.
go run ./cmd/friday

# 2. Drop your DeepSeek API key into ~/.friday/.env
mkdir -p ~/.friday
cat > ~/.friday/.env <<'EOF'
DEEPSEEK_API_KEY=sk-...
LOG_LEVEL=info
EOF

# 3. Run again.
go run ./cmd/friday
```

## Configuration

friday looks at these env vars (set them in `~/.friday/.env`):

| Variable             | Default     | Notes                                          |
|----------------------|-------------|------------------------------------------------|
| `DEEPSEEK_API_KEY`   | (required)  | DeepSeek API bearer token.                     |
| `LOG_LEVEL`          | `info`      | `debug` / `info` / `warn` / `error`.           |
| `LOG_DIR`            | `~/.friday/logs` | Set to empty string to disable file logs. |
| `MAX_ITERS`          | `30`        | Agent loop iteration cap per Run().            |
| `LOGLEVEL` / `LOGDIR`| —           | Friday-flavoured aliases for the above two.    |

The `~/.friday/config/friday-config.yml` file is auto-generated on first run
and holds non-secret defaults (provider, model, etc).

## Keybindings

| Key      | Action                                                         |
|----------|----------------------------------------------------------------|
| `Enter`  | Send the prompt.                                               |
| `Ctrl+C` | If a Run is in flight: cancel it. Otherwise: quit.             |
| `Ctrl+L` | Clear the transcript.                                          |
| `Esc`    | Quit immediately.                                              |
| `↑` / `↓`| Scroll the transcript.                                         |

## Project layout

```
cmd/friday/main.go              entry point
internal/bootstrap/             cfg, agent, persona
internal/tui/                   bubbletea Model + Sink + render
internal/tool/                  friday's own custom tools (e.g. echo)
docs/sdk-feedback.md            evva SDK rough-edge notes
```

`internal/` is friday-private. Only the evva `pkg/*` packages are imported.

## Custom tools

Friday demonstrates the SDK's tool-extension surface by shipping its
own `echo` tool — a trivial example that echoes input back unchanged.
The wiring is two parts:

1. **Implement the `pkg/tools.Tool` interface** (Name, Description,
   Schema, Execute). See `internal/tool/echo.go` — about 100 LOC
   including doc comments and the JSON schema string.
2. **Register at agent construction** via `agent.WithCustomTool` and
   append the name to the active list:

```go
active, deferred := kits.GeneralPurposeKit()
active = append(active, fridaytool.EchoToolName)

ag, _ := agent.NewWithProfile(prof,
    // …
    agent.WithCustomTool(fridaytool.EchoToolName, func(pkgtools.State) (pkgtools.Tool, error) {
        return fridaytool.NewEcho(), nil
    }),
)
```

To exercise the tool from the TUI, prompt friday with something like
*"echo `hello world` three times"* — the model will see the `echo`
tool in its catalog and dispatch it.

To add a tool of your own, drop a sibling file next to `echo.go`,
mirror the same shape, and wire it through the same two-line
register-and-append pattern.
