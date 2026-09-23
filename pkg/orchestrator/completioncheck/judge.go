package completioncheck

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"m31labs.dev/buckley/pkg/model/decisions"
)

// DecisionsEndpoint and DecisionsTimeout are fixed. The guard's provider
// and privacy posture is never redirected by project or user
// configuration; see docs/COMPLETION_GUARD.md.
const (
	DecisionsEndpoint = decisions.DefaultEndpoint
	DecisionsTimeout  = 12 * time.Second
)

// completionQuestions are the four independent yes/no probability
// questions the guard asks about one (request, response) pair in a single
// Decisions call. Their keys match the Evidence fields Decide reads.
var completionQuestions = map[string]decisions.Question{
	"action": {
		Type: decisions.TypeNoul,
		Instructions: "Does request ask the assistant to perform concrete work, rather than only " +
			"explain, advise, discuss, plan, or answer a question? Requests like 'can you implement' " +
			"are action requests.",
	},
	"unfinished": {
		Type: decisions.TypeNoul,
		Instructions: "Does response explicitly leave a required part of request undone, such as " +
			"proposing the requested implementation, offering to do it later, or reporting a required " +
			"verification not yet performed? Optional suggestions and follow-on improvements do not " +
			"count. A brief report of completion is not evidence of missing work.",
	},
	"authorized": {
		Type: decisions.TypeNoul,
		Instructions: "Does request already ask for the unfinished implementation, correction, or " +
			"verification described in response? Judge authorization from the user request. If the " +
			"user already requested it, an assistant offer such as I can do it next or if you want " +
			"does not revoke that authorization. Optional extra improvements not requested by the " +
			"user are not authorized.",
	},
	"needs_user": {
		Type: decisions.TypeNoul,
		Instructions: "Is there a genuine stated reason the assistant must wait for a person or stop: " +
			"an explicit user pause/cancellation, a user-imposed approval requirement, necessary " +
			"missing information or credentials, a safety refusal, an external dependency, or an " +
			"exhausted budget? Answer no for a redundant offer to perform work the user already " +
			"requested, including if you want. Unperformed local implementation or tests alone do " +
			"not require a person.",
	},
}

// decisionsJudge adapts a general decisions.Client to the Judge interface
// Check/Decide expects.
type decisionsJudge struct {
	client *decisions.Client
}

// NewOpenRouterJudge builds the guard's fixed OpenRouter Decisions Judge.
// Model, endpoint, and provider privacy posture (ZDR, data collection
// denied, fallbacks disabled -- see pkg/model/decisions.Client.Ask) are
// pinned by this constructor, not by configuration: no project or user
// configuration layer can redirect this request to a different model,
// endpoint, or privacy policy. pricing supplies the cost fallback the
// underlying client uses only when a response omits its own usage.cost.
func NewOpenRouterJudge(apiKey string, pricing decisions.Pricing) (Judge, error) {
	client, err := decisions.New(decisions.Config{
		APIKey: apiKey, Model: Model, Endpoint: DecisionsEndpoint,
		Timeout: DecisionsTimeout, Pricing: pricing,
	})
	if err != nil {
		return nil, err
	}
	return decisionsJudge{client: client}, nil
}

func (j decisionsJudge) Judge(ctx context.Context, input Input) (Evidence, error) {
	state, err := json.Marshal(input)
	if err != nil {
		return Evidence{}, err
	}
	resp, err := j.client.Ask(ctx, string(state), completionQuestions)
	if err != nil {
		return Evidence{}, err
	}
	noul := func(key string) (float64, error) {
		answer, ok := resp.Answers[key]
		if !ok || answer.Type != decisions.TypeNoul {
			return 0, fmt.Errorf("missing typed decision answer: %s", key)
		}
		return answer.Noul, nil
	}
	action, err := noul("action")
	if err != nil {
		return Evidence{}, err
	}
	unfinished, err := noul("unfinished")
	if err != nil {
		return Evidence{}, err
	}
	authorized, err := noul("authorized")
	if err != nil {
		return Evidence{}, err
	}
	needsUser, err := noul("needs_user")
	if err != nil {
		return Evidence{}, err
	}
	return Evidence{
		Action: action, Unfinished: unfinished, Authorized: authorized, NeedsUser: needsUser,
		Model: resp.Model, InputTokens: resp.Usage.InputTokens, Cost: resp.Usage.Cost,
	}, nil
}
