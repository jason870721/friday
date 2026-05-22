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
docs/sdk-feedback.md            evva SDK rough-edge notes
```

`internal/` is friday-private. Only the evva `pkg/*` packages are imported.
