// Package rlm implements Buckley's coordinator–worker execution runtime.
//
// Runtime.Execute is the full coordinator–worker topology: a coordinator model
// plans the work, delegates only bounded missing work to sub-agents, reads
// shared scratchpad evidence, and synthesizes the final answer. The dispatcher
// owns admission, model routing, concurrency limits, and cooperative per-task
// deadlines. Workers return public results, usage, and execution evidence.
//
// Runtime.ExecuteTasks is deliberately narrower. The CLI plan runner uses that
// host-bound worker-batch path, consumes per-task records, and keeps execution
// success distinct from host verification; it does not run the coordinator
// iteration/synthesis loop or read coordinator scratchpad summaries. Structured
// checks can verify supported claims, while legacy prose criteria remain
// unverified until a host executes a trusted check. Scratchpad entries are
// inspectable evidence, not authority.
//
// The historical "rlm" package path, configuration keys, telemetry event names,
// and import names remain for compatibility; the runtime is best described by
// its actual coordinated execution / coordinator–worker methodology rather than
// as a new acronym.
//
// Compatibility notes:
//   - CoordinatorConfig.StreamPartials controls coordinator progress
//     publication to iteration hooks and rlm iteration telemetry. It does not
//     enable text-token streaming or alter the terminal Execute result.
//   - SubAgentRuntimeConfig.Timeout is a cooperative per-active-task deadline.
//     The timer starts after concurrency/rate queueing, spans retries for that
//     task, defaults to five minutes, and remains subordinate to any parent
//     context deadline. It cancels cooperative work; it does not forcibly
//     preempt code that ignores context cancellation.
//   - The coordinator emits rlm.budget_warning when the token ceiling returns
//     an incomplete answer. This is intentionally narrow: it does not promise a
//     warning for every reserve or finalization condition.
package rlm
