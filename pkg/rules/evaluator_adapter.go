package rules

import "github.com/odvcencio/buckley/pkg/types"

// EngineAdapter wraps *Engine to implement types.RuleEvaluator.
type EngineAdapter struct{ engine *Engine }

func NewEngineAdapter(engine *Engine) *EngineAdapter {
	return &EngineAdapter{engine: engine}
}

func (a *EngineAdapter) EvalStrategy(domain, name string, facts map[string]any) (types.StrategyResult, error) {
	result, err := a.engine.EvalStrategy(domain, name, facts)
	if err != nil {
		return types.StrategyResult{}, err
	}
	return types.StrategyResult{Params: result.Params}, nil
}
