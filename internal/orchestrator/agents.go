package orchestrator

import (
	"github.com/johnny1110/evva/pkg/agent"
	"github.com/johnny1110/evva/pkg/config"
	"github.com/johnny1110/evva/pkg/constant"
	"github.com/johnny1110/evva/pkg/event"
	pkgtools "github.com/johnny1110/evva/pkg/tools"
)

// buildAgent constructs one role agent: a buffered DeepSeek profile with
// the given system prompt and a sink that tags every event with role, plus
// the per-role custom tools passed as options. Active tools are left empty
// — the SDK auto-registers each WithCustomTool into the active catalog, so
// each agent ends up with exactly its disjoint tool set and nothing else.
func buildAgent(cfg *config.Config, name, role, systemPrompt string, emitter RoleEmitter, maxIters int, model constant.Model, effort string, toolOpts ...agent.Option) (agent.Agent, error) {
	prof, err := agent.NewProfile(
		name,
		systemPrompt,
		[]pkgtools.ToolName{}, // custom tools auto-add to the active catalog
		"deepseek",
		model,
		agent.ProfileOptions{Stream: false},
	)
	if err != nil {
		return nil, err
	}

	opts := []agent.Option{
		agent.WithConfig(cfg),
		agent.WithSink(roleSink(emitter, role)),
		agent.WithMaxIterations(maxIters),
		agent.WithHeadlessBypass(),
		agent.WithName(name),
	}
	opts = append(opts, toolOpts...)

	ag, err := agent.NewWithProfile(prof, opts...)
	if err != nil {
		return nil, err
	}
	if err := ag.SetEffort(effort); err != nil {
		return nil, err
	}
	return ag, nil
}

// customTool wraps a friday tool constructor into a WithCustomTool option.
// The factory ignores tools.State — friday's binance tools reach their
// shared client through package state, not the agent.
func customTool(name pkgtools.ToolName, make func() pkgtools.Tool) agent.Option {
	return agent.WithCustomTool(name, func(pkgtools.State) (pkgtools.Tool, error) {
		return make(), nil
	})
}

// submitOption registers a role's structured-output capture tool, closing
// over the capture slot the orchestrator reads after the run.
func submitOption(name, desc, schema string, cap *capture) agent.Option {
	return agent.WithCustomTool(pkgtools.ToolName(name), func(pkgtools.State) (pkgtools.Tool, error) {
		return newSubmitTool(name, desc, schema, cap), nil
	})
}

// roleSink adapts the RoleEmitter to an event.Sink that tags every event
// from this agent with its role.
func roleSink(emitter RoleEmitter, role string) event.Sink {
	return event.SinkFunc(func(e event.Event) {
		emitter.EmitRole(role, e)
	})
}
