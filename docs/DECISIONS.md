# Decisions gating

`pkg/model/decisions` calls OpenRouter's Decisions API. The endpoint
answers typed questions; it does not run chat completions. Buckley uses an
answer only to gate or route otherwise-expensive work, never as proof of
correctness.

## The model

The served model is TypeSafe Jev (`typesafe/jev-1.13`, currently served as
`typesafe/jev-1.13-20260917`). It has a 32k context window. Input costs
$0.042 per million tokens; output is free. An internal investigation found
it about 33 times cheaper and 3.5 times faster than Buckley's default
review model. The same investigation found it missed one obvious
certain-panic defect. Treat a Decisions answer as a probability, not a
verdict. A reviewer or a human stays the authority for correctness.

## Question types

A single Decisions call can ask several typed questions at once:

- **noul**: a yes/no probability, in [0, 1].
- **choice**: a pick among named options, each with a probability.
- **score**: a position on an ordered scale (for example
  trivial/light/standard/deep), with a probability for every label.

`pkg/model/decisions.Client.Ask` sends one request per call and fails
closed: a missing answer, an out-of-range probability, an unrecognized
choice, or an unexpected served model all return an error instead of a
partial result.

## Cost accounting

Every `Ask` call returns a `Usage` with a populated `Cost`. The client
prefers the response's own `usage.cost`. It falls back to configured
per-million-token pricing only when the response omits `usage.cost`. A
Decisions call is never priced as "unknown" (see
`pkg/model/cost_bounded.go` for the same discipline applied to
chat-completions pricing).

## Configuration

```yaml
decisions:
  enabled: false
  model: typesafe/jev-1.13
  endpoint: https://openrouter.ai/api/alpha/decisions
  timeout: 12s
  pricing:
    input_per_million: 0.042
    output_per_million: 0
  log_path: ""  # empty uses ~/.buckley/decisions.jsonl
  gates:
    review_depth:
      enabled: false
      trivial_probability: 0.85
    reasoning_choice:
      enabled: false
```

Everything defaults off. Setting `decisions.enabled: true` alone changes
no behavior; each gate below also needs its own `enabled: true`.

Environment overrides follow Buckley's usual convention:
`BUCKLEY_DECISIONS_ENABLED`, `BUCKLEY_DECISIONS_MODEL`,
`BUCKLEY_DECISIONS_ENDPOINT`, `BUCKLEY_DECISIONS_TIMEOUT`,
`BUCKLEY_DECISIONS_PRICING_INPUT_PER_MILLION`,
`BUCKLEY_DECISIONS_PRICING_OUTPUT_PER_MILLION`,
`BUCKLEY_DECISIONS_LOG_PATH`,
`BUCKLEY_DECISIONS_GATE_REVIEW_DEPTH_ENABLED`,
`BUCKLEY_DECISIONS_GATE_REVIEW_DEPTH_TRIVIAL_PROBABILITY`, and
`BUCKLEY_DECISIONS_GATE_REASONING_CHOICE_ENABLED`.

A Decisions call reuses `providers.openrouter.api_key`; it needs no
separate credential.

## Gate 1: review depth

Before `buckbot`/`review-pr` runs its reviewer model, this gate asks a
`score` question: trivial, light, standard, or deep. The question's
evidence is the diff stats (files changed, additions, deletions) and a
truncated diff, at most 4 KB.

A "trivial" answer at or above `decisions.gates.review_depth.trivial_probability`
(default 0.85) lowers the reviewer's reasoning effort by one step, for
example `high` to `medium`.

This gate never skips a review verdict. It never relaxes what evidence a
review must collect: `--depth` and its verification requirements stay
unaffected. The reviewer stays the only judge of pass or fail. Pass
`--full-depth` to `review-pr` to disable the gate for one run and always
review at full configured reasoning.

## Gate 2: reasoning choice

Buckbot's `reasoning: auto` setting (the shipped default) otherwise
resolves to a fixed effort. When this gate is enabled, Buckley instead
asks a `choice` question -- low, medium, or high -- and uses the answer.
On any gate failure, timeout, or disabled configuration, resolution falls
back to the existing default unchanged.

## Calibration log

Every gate decision -- the Decisions scores or choice probabilities, and
the depth or effort Buckley chose -- is appended as one JSON line to
`decisions.log_path` (default `~/.buckley/decisions.jsonl`). Review this
log against real outcomes before raising a threshold or trusting a gate
more.

## The completion guard

`buckley completion-check --hook` (see `docs/COMPLETION_GUARD.md`) is a
separate, always-available Codex hook built on the same
`pkg/model/decisions` client. It asks four fixed `noul` questions with a
hardcoded model, endpoint, and privacy policy that no configuration layer
can redirect. It is unaffected by `decisions.enabled`: the guard has its
own on/off switch (`BUCKLEY_COMPLETION_GUARD=off`, or a `disabled` file in
its state directory).

## Limits

A Decisions answer is a cheap, fast probability estimate from a small
model, not a certified verdict. Use it only to gate or route work whose
correctness is checked elsewhere -- a reviewer, a test suite, or a
person. Do not use a Decisions answer as the sole basis for skipping
review, skipping tests, or accepting a change.
