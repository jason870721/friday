# evva SDK feedback — from building friday

Notes collected while building friday against successive evva tags.
Each round is a concrete, actionable list — every item names the
file/line in evva where the rough edge lives, and what would smooth
it.

- **Round 1** (`v0.2.4-alpha.1`): initial build — 12 findings, all
  resolved by evva's Phase 19. Kept below as the historical record.
- **Round 2** (`v0.2.4-alpha.2`): post-Phase-19 migration + day-2
  rough edges. See the **Round 2** heading near the bottom.

Loose categories:

- **Naming**: identifier choices that surprised me as a consumer.
- **Discoverability**: things that work fine once you know them but
  aren't documented or hinted at in code.
- **Defaults**: factory defaults that point at evva itself rather than
  the calling AppName.
- **Ergonomics**: missing accessors / helpers that the consumer ends up
  hand-rolling.

---

## Findings

### 1. Naming — `event.ErrorPayload.Err` is `error`, not `string`

`pkg/event/event.go:237`

```go
type ErrorPayload struct {
    Stage string
    Err   error
}
```

Consumer code reads `e.Error.Err.Error()` to stringify. Caught me on
the first render-event pass — I wrote `if e.Error.Err != ""` (treating
it as a string). Either:

- Rename to `ErrMsg string` (matches the rest of the payloads, which
  are all stringy), or
- Document the contract in the doc comment.

### 2. Naming — `IterLimitPayload.Reached` vs `RunEndPayload.Iters`

`pkg/event/event.go:151` and `:142`

```go
type RunEndPayload   struct { Iters int; ... }
type IterLimitPayload struct { Reached int }
```

Same concept (loop iteration count), two field names. Pick one.

### 3. Defaults — first-run YAML always writes `default_profile: evva`

`pkg/config/load.go` → `LoadFileConfig` → seed config

When friday calls `config.Load(LoadOptions{AppName: "friday", ...})`,
the generated `~/.friday/config/friday-config.yml` still contains:

```yaml
default_profile: evva
```

It's harmless (friday calls `NewWithProfile` so the YAML default is
never used), but it's a confusing artefact for a user inspecting their
friday-flavoured config. Suggested fix: `LoadOptions.AppName` propagates
into the seeded YAML's `default_profile`, with a fallback to `evva`
when the field is empty.

### 4. Defaults — `WithPermissionMode("bypass")` is mandatory for a
   non-evva consumer, but the requirement is buried

`pkg/agent/new_with_profile.go:36-42`

The default permission broker auto-DENIES every approval request. For a
TUI like friday that doesn't render an approval overlay, every tool
call needing approval (bash that the classifier doesn't auto-allow,
write/edit in `ModeDefault`, etc.) silently turns into an error.

The minimal-host example does pass `WithPermissionMode("bypass")`, but
this is the kind of footgun that needs a louder pointer:

- An `agent.WithHeadlessBypass()` helper that bundles
  `WithPermissionMode("bypass")` with a comment about "no approval UI
  means tool calls auto-succeed; only use in trusted environments."
- Or `NewWithProfile` could log `slog.Warn` once on first
  `BehaviorDeny` if the caller never installed a real broker.

### 5. Ergonomics — `cfg.LLMProviderConfig` is a public map; expected
   pattern is direct assignment

`pkg/config/config.go` and `examples/minimal-host/main.go:98`

```go
cfg.LLMProviderConfig["deepseek"] = config.APIConfig{
    ApiURL:    "https://api.deepseek.com",
    ApiSecret: apiKey,
    Models:    []constant.Model{constant.DEEPSEEK_V4_PRO},
}
```

This works but feels low-level. Three friction points:

- `Models []constant.Model` is duplicative with `cfg.DefaultModel`
  (friday only uses one model — having to list it in `Models` too is
  bookkeeping with no purpose).
- The map slot doesn't validate on write — a typo in the provider key
  silently registers nothing.
- There's no setter that mutates under `cfg.mu` — concurrent writes
  from two goroutines would race.

Suggested helper:

```go
func (c *Config) SetProviderCredentials(name, apiURL, apiKey string) error
```

### 6. Ergonomics — friday composes the "general-purpose toolkit" by
   hand from per-family `Names()`

`/mnt/friday/internal/bootstrap/bootstrap.go:60-66`

```go
active = append(active, fs.Names()...)
active = append(active, shell.Names()...)
active = append(active, todo.Names()...)
active = append(active, util.Names()...)
active = append(active, tools.TOOL_SEARCH)
```

This works but every downstream consumer wanting a "general coding
agent" duplicates the same boilerplate. A `tools.GeneralPurposeKit()`
helper in `pkg/tools/` (returning the canonical evva general-purpose
active+deferred lists) would let friday write:

```go
active, deferred := tools.GeneralPurposeKit()
```

Document the kit composition so consumers can copy + tweak.

### 7. Ergonomics — `agent.NewProfile` model arg is `string`, not
   `constant.Model`

`pkg/agent/profile.go:62`

```go
func NewProfile(name, systemPrompt string, activeTools []tools.ToolName,
    providerName, model string, opts ProfileOptions) (Profile, error)
```

Friday already has the typed `constant.DEEPSEEK_V4_PRO` constant and
has to call `string(...)` on it:

```go
agent.NewProfile("friday", SystemPrompt, active,
    "deepseek", string(constant.DEEPSEEK_V4_PRO),
    agent.ProfileOptions{...})
```

Accepting either `string` or `constant.Model` (e.g. by accepting a
`constant.Model` and converting `string` callers with `constant.Model(s)`
at the boundary) would be a tiny QoL win.

### 8. Discoverability — bubbletea / bubbles version pinning is implicit

evva's `go.mod` requires:

- `github.com/charmbracelet/bubbletea v1.3.10`
- `github.com/charmbracelet/bubbles v1.0.0`
- `github.com/charmbracelet/lipgloss v1.1.1-...`

Friday pinned the same versions explicitly so its `tea.Program` type
matches the one evva expects on `pkg/ui.UI.Run`. A downstream that
naively `go get`s the latest bubbletea risks a subtle type-mismatch on
the program handle if evva later upgrades. Document the version
contract in `docs/extending.md`.

### 9. Ergonomics — env-var loading is opinionated

`pkg/config/load.go:76` calls `godotenv.Load(appHome + "/.env")` and
then reads a fixed list of canonical names: `APP_ENV`, `LOG_LEVEL`,
`LOG_DIR`, `LOG_FORMAT`, `SKILLS_DIR`, `USER_PROFILE`,
`EVVA_AUTO_MEMORY`.

Friday wanted to accept friendlier aliases (`LOGDIR`, `LOGLEVEL`,
`MAX_ITERS`). The current solution is to translate before/after
`Load`:

```go
// Before Load: promote aliases into canonical names so godotenv sees
// them.
if v := os.Getenv("LOGDIR"); v != "" && os.Getenv("LOG_DIR") == "" {
    os.Setenv("LOG_DIR", v)
}

// After Load: apply config-shaped overrides for vars evva doesn't
// natively read.
if v := os.Getenv("MAX_ITERS"); v != "" { ... cfg.SetMaxIterations(n) ... }
```

A `LoadOptions.EnvAliases map[string]string` field that lets the
consumer map their preferred names to evva's canonicals would replace
the pre-Load shim entirely. And `LoadOptions.EnvOverrides
map[string]func(*Config) error` (or similar) for vars without a YAML
hook would replace the post-Load shim.

### 10. Discoverability — `event.Event` payload field selection
   requires reading source

Twenty `Kind` constants, twenty `*Payload` pointer fields on `Event`,
no comment-table mapping them. Consumers grep `pkg/event/event.go` to
discover that `KindToolUseStart` → `e.ToolUseStart`. A
`func (e Event) Payload() any` switch would be a wrist-saving accessor
for callers who only want the "the thing that goes with this kind":

```go
switch p := e.Payload().(type) {
case *event.TextPayload:    // ...
case *event.ToolUseStartPayload: // ...
}
```

Strictly redundant — the field-pointer pattern is fine once you know
it — but a clear `Payload()` helper in the doc would be a nice
on-ramp.

### 11. Ergonomics — `agent.Agent.Session()` returns `SessionInfo`
   with `MessageCount` but it's not in the public docstring

`pkg/agent/agent.go:118-127` shows the SessionInfo conversion:

```go
return SessionInfo{
    MessageCount:    len(s.GetMessages()),
    InputTokens:     u.InputTokens,
    OutputTokens:    u.OutputTokens,
    LastInputTokens: s.LastTurnInputTokens(),
}
```

Friday's status footer uses `MessageCount` to show "12 msgs" — but the
field has no doc comment in `types.go`. Add a one-line comment per
field so editors surface them.

### 12. Naming — `WithPermissionMode` accepts a string, but a typo
   silently degrades to default

`pkg/agent/agent.go:69-73`:

```go
if cfg.PermissionMode != "" {
    if m, ok := permission.ParseMode(cfg.PermissionMode); ok {
        permMode = m
    }
}
```

If the consumer types `"by-pass"` (with a dash) the agent silently
falls back to `ModeBypass` (the seed default for `agent.New`) or to
whatever the profile carried. No log, no error. A typed parameter
(`agent.PermissionMode` enum exported from `pkg/agent`) would catch
the typo at compile time.

---

## What worked nicely

For balance — these were genuinely smooth:

- The `examples/minimal-host/main.go` template covers ~80% of friday's
  setup. Reading it end-to-end gave me the wiring pattern for sink,
  Profile, agent in about 5 minutes.
- `_ "github.com/johnny1110/evva/pkg/llm/builtins"` for blank-import
  provider registration is exactly the right idiom — clean and
  Go-native.
- `agent.NewWithProfile` separating Profile construction from agent
  construction means tests can build profiles without an LLM client.
- `event.Sink` is a tiny one-method interface — easy to satisfy from a
  bubbletea `tea.Program` holder.
- The published evva tag (`v0.2.4-alpha.1`) resolves cleanly via
  `go mod tidy`; no replace directive needed.

---

# Round 2 — evva v0.2.4-alpha.2 (post-Phase-19)

evva landed Phase 19 (SDK Support sweep) and tagged v0.2.4-alpha.2.
Friday migrated by running `go get github.com/johnny1110/evva@v0.2.4-alpha.2`,
fixing the breaking changes, and adopting the new helpers. This
section documents:

1. How each Round 1 finding was resolved.
2. What the migration actually felt like.
3. Fresh rough edges discovered when using the new APIs day-to-day.

## Round 1 resolution scorecard

| # | Finding | Phase 19 fix | Verified |
| --- | --- | --- | --- |
| 1 | `ErrorPayload.Err` is `error`, not `string` | Added `Message string` sibling, populated at emit time | ✅ — render.go now does `p.Message` instead of nil-check + `.Error()` |
| 2 | `IterLimitPayload.Reached` vs `RunEndPayload.Iters` | Collapsed to `Iters` (Reached removed entirely in dev-mode cleanup) | ✅ — one-line caller update |
| 3 | First-run YAML hardcoded `default_profile: evva` | `LoadOptions.AppName` propagates to the seed | ✅ — `~/.friday/config/friday-config.yml` now writes `default_profile: friday` |
| 4 | `WithPermissionMode("bypass")` is buried | `agent.WithHeadlessBypass()` convenience | ✅ — call site reads as English |
| 5 | `LLMProviderConfig` direct map assignment | `Config.SetProviderCredentials(name, url, key)` | ✅ — friday uses it via `EnvOverrides` |
| 6 | Hand-composed general-purpose kit | `pkg/tools/kits.GeneralPurposeKit()` | ✅ — friday's bootstrap shrunk by 20 LOC |
| 7 | `NewProfile` model arg is `string` | Now `constant.Model` (typed) | ✅ — friday drops the `string(...)` cast |
| 8 | bubbletea / bubbles / lipgloss version pinning implicit | Documented in `docs/extending.md` | ✅ — friday's go.mod matches the table |
| 9 | Env-var loading is opinionated | `LoadOptions.EnvAliases` + `EnvOverrides` | ✅ — friday's bootstrap has zero pre/post shim functions |
| 10 | Event payload field discovery | `Event.Payload() any` helper | ✅ — render.go's switch is cleaner |
| 11 | `SessionInfo` no field docs | Doc-commented every field | ✅ — editor hover surfaces them now |
| 12 | `WithPermissionMode` silent typo | Typed `agent.PermissionMode` enum | ✅ — `agent.PermissionBypass` is the wire-callable form |

**12 of 12 resolved.** Phase 19 cleared the whole Round 1 backlog.

## Migration experience

The dev-mode no-deprecation cleanup made the upgrade trivial:

```
$ go get github.com/johnny1110/evva@v0.2.4-alpha.2
go: upgraded github.com/johnny1110/evva v0.2.4-alpha.1 => v0.2.4-alpha.2

$ go build ./...
internal/bootstrap/bootstrap.go:92:3: cannot use string(constant.DEEPSEEK_V4_PRO)
  (constant "deepseek-v4-pro" of type string) as constant.Model value in argument
  to agent.NewProfile
internal/tui/render.go:84:20: e.IterLimit.Reached undefined (type *event.IterLimitPayload
  has no field or method Reached)
```

Exactly two compile errors, both with crisp messages pointing right at
the new types. The `CHANGELOG.md` "Breaking" section spelled out the
one-line migration for each. From `go get` to a passing build was
about 90 seconds of editing — the smallest plausible upgrade cost.

The bigger win came from **adopting the new helpers** rather than just
patching the compile errors. Friday's bootstrap shrunk from ~160 LOC
to ~135 LOC (–15%); render.go's main switch shrunk from 7 cases with
nil-checks to 5 cases without. Both are now readable as English: the
Profile constructor names a typed model, the agent option says
`WithHeadlessBypass`, and the env-var dance is declared inside
`LoadOptions` rather than scattered around `Load()` calls.

## Round 2 findings

Day-2 rough edges discovered while actually USING the new APIs.

### R2-1. Ergonomics — `EnvOverrides` lack a "name" for diagnostics

`pkg/config/load.go` Phase 19b.

`LoadOptions.EnvOverrides` is `[]func(*Config) error`. When an
override returns an error, evva wraps it as `config: EnvOverrides:
<wrapped>` and short-circuits. Useful, but in a real downstream app
with 3–5 overrides, "config: EnvOverrides: max_iters_too_large" tells
you nothing about WHICH override fired.

Friday's bootstrap currently passes two overrides:

```go
EnvOverrides: []func(*config.Config) error{
    applyMaxItersFromEnv,
    applyDeepSeekCreds,
},
```

A named record would make failure modes self-describing:

```go
type EnvOverride struct {
    Name string
    Fn   func(*Config) error
}

EnvOverrides: []config.EnvOverride{
    {Name: "max_iters_from_env", Fn: applyMaxItersFromEnv},
    {Name: "deepseek_creds",     Fn: applyDeepSeekCreds},
}
```

The wrapped error would then read
`config: EnvOverrides[deepseek_creds]: api key validation failed`.
Optional but cheap.

### R2-2. Ergonomics — provider-credentials wiring is still two-step

After Phase 19, friday wires DeepSeek credentials as:

```go
EnvAliases: map[string]string{"APIKEY": "DEEPSEEK_API_KEY", ...},
EnvOverrides: []func(*config.Config) error{
    applyDeepSeekCreds, // reads DEEPSEEK_API_KEY env, calls cfg.SetProviderCredentials
},
```

So the env var name is mapped, then a separate function reads it and
calls the setter. For a downstream app that just wants "promote APIKEY
into DeepSeek creds," this is three indirections. A declarative
option would be tidier:

```go
LoadOptions{
    ProviderCredentials: map[string]config.ProviderCredsFromEnv{
        "deepseek": {APIKeyEnv: "DEEPSEEK_API_KEY", APIURLEnv: "DEEPSEEK_API_URL"},
    },
}
```

Implementation: `Load` reads the env vars (after EnvAliases run) and
calls `cfg.SetProviderCredentials` for each entry. Removes the
EnvOverride boilerplate for the most common case (90% of downstream
apps wire one provider's creds from one env var).

### R2-3. Discoverability — first-run UX has a chicken-and-egg

When friday first launches on a fresh `~/.friday/`, the user sees:

```
friday: built on evva v0.3.0-alpha.1
friday: wrote new config to /root/.friday/config/friday-config.yml — fill in your API keys to use cloud providers.
friday: DEEPSEEK_API_KEY is empty — set it in ~/.friday/.env and try again.
```

The first two lines are evva-driven (auto-create YAML); the third is
friday-driven. But `~/.friday/.env` doesn't exist after the first run
— evva creates the YAML but not the `.env`. The user has to know to
create `~/.friday/.env` themselves.

A `LoadOptions.SeedEnvTemplate string` field with a default template
to write next to the YAML on first run would close the loop:

```go
LoadOptions{
    AppName: "friday",
    SeedEnvTemplate: `# friday env vars
DEEPSEEK_API_KEY=
LOG_LEVEL=info
MAX_ITERS=30
`,
}
```

On first run, evva writes both the YAML and `<AppHome>/.env` (the
latter only if missing), leaving the user with a complete config tree
to edit. No big deal, but a nice on-ramp.

### R2-4. Ergonomics — `kits.GeneralPurposeKit()` always includes `TOOL_SEARCH`

`pkg/tools/kits/kits.go` Phase 19d.

`GeneralPurposeKit()` adds `TOOL_SEARCH` to active alongside fs +
shell + todo + util. That's correct when the kit ships deferred tools
(web), because the model needs `tool_search` to discover them. But a
consumer that uses `GeneralPurposeKit` and DOESN'T pass the deferred
slice to `agent.NewProfile` ends up with `tool_search` active but
nothing to search for — pure noise.

Either:

- Document that `TOOL_SEARCH` is paired with the deferred return value
  (and consumers who drop deferred should also drop tool_search);
- Or split: `GeneralPurposeActive() []ToolName` returns active WITHOUT
  tool_search; `GeneralPurposeKit()` returns both lists plus
  tool_search. Composition is then explicit.

Minor; doesn't affect friday since it does use the deferred list.

### R2-5. Documentation — `pkg/version.String()` returns `v0.3.0-alpha.1`

`pkg/version/version.go` Phase 19f.

The leading `v` prefix is conventional in git tags but unconventional
in log lines. Some Go libs return the bare semver and let the caller
add `v` (or `release/`). Friday's startup line reads
`friday: built on evva v0.3.0-alpha.1`, which works, but if the user
wants `0.3.0-alpha.1` alone they have to `strings.TrimPrefix(v, "v")`.

Either:

- Add a sibling `Bare() string` returning `Version` verbatim;
- Or rename `String()` to `Tag()` and add `String()` returning bare semver.

Minor. Today's behaviour is fine.

### R2-6. Future work — broker promotion is still deferred

Phase 19c explicitly deferred promoting `WithPermissionBroker` and
`WithQuestionBroker` to `pkg/agent`, pending a `PermissionPrompter`
callback design. Friday doesn't need this yet (bypass mode), so it
wasn't blocked, but a friday-style TUI with a real approval overlay
would need this seam.

Suggested shape (recap from CLAUDE.md):

```go
type PermissionRequest struct { /* tool name, input, mode, reason */ }
type PermissionPrompter func(ctx context.Context, req PermissionRequest) PermissionDecision

func WithPermissionPrompter(p PermissionPrompter) Option { ... }
```

The function-shaped callback is friendlier than a broker interface
and doesn't expose internal/permission types. Tracked.

## What worked exceptionally well in Round 2

Highlighting the wins so the next round of polish keeps them:

- **`go get @v0.2.4-alpha.2` + 90 seconds of editing** — the entire
  Phase 19 surface change landed in friday with two file edits. The
  CHANGELOG's "Breaking" section was a complete migration guide.
- **`kits.GeneralPurposeKit()` is the single biggest readability win.**
  6 lines of `append` chains → 1 line. Every future downstream app
  saves the same lines.
- **`LoadOptions.EnvOverrides` is the right shape.** Closures with
  cfg pointer + first-error-short-circuits maps cleanly onto a
  per-app "translate my env-var conventions to evva's" function.
  Friday's bootstrap now has ZERO helper functions for env-var
  munging — every translation is declared inline in `LoadOptions`.
- **`Event.Payload()` switch eliminates the kind-field correspondence
  table.** Type-switching on `*event.TextPayload` etc. is also
  exhaustive enough that an editor can autocomplete payload field
  access — the old `e.Text.Text` pattern needed the developer to
  remember the field name.
- **`WithHeadlessBypass()` is the discoverability hit of Phase 19c.**
  The name itself documents the use case; a reviewer reading the
  bootstrap immediately knows "this app has no approval UI." Even
  better than the typed `WithPermissionMode(PermissionBypass)`.
- **`pkg/version.String()` on startup** is a tiny but reassuring
  addition. Users filing bugs include the SDK version automatically.
