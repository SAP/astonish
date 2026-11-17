# Bot final-synthesis token reserve

## Purpose

The synthesis reserve is a stopping policy on the existing run budget, not a
second allowance. It should stop further research while a tools-disabled answer
from retained evidence can still pass normal model admission. A token-admission
failure after switching to final mode is not successful synthesis.

## Estimation and admission

The factory asks the wrapped model for its request-scoped input estimate.
`botProgressLLM` forwards this query to the same `BudgetLLM` estimator used for
admission; enabling `progress_v1` must not revert reserve decisions to a separate
cold estimator. Provider-reported prompt usage can bound an unchanged prefix,
while appended evidence is charged conservatively. Replacing prior history or
changing the model configuration invalidates reuse of that measurement.

Reserve guidance is appended as request-local user context. It does not mutate
the caller's history or stable system instructions. Changing remaining-budget
text must not repeatedly discard the useful measured history prefix.

The stopping threshold needs room for both the next research request and a
subsequent final request, including their output ceilings and conservative
guidance bounds. The final request removes tools and tool configuration. That
configuration change can invalidate the research measurement, so the final-input
allowance must cover its cold estimate rather than assume the cheaper research
estimate applies twice. Arithmetic must remain overflow-safe.

Once final mode begins, it is sticky: no further research, delegation, or tool
execution is allowed. All model calls still use the original budget's
reservation and settlement checks. Progress dispatch, generation health checks,
live authorization, and final-answer validation remain in the call path. A
reserve cannot promise completion after arbitrary external spending, enormous
new evidence, or provider failure; normal admission must still fail closed when
the remaining budget cannot cover the request.

## Trust boundary

Only a fully consumed, successfully settled response with positive authoritative
prompt usage may establish a measured prefix. This is not a learned bytes/token
ratio or a cached-input discount. Changed model, system instructions, tools,
configuration, or history invalidates the observation. Media and cached content
still require a model-aware estimator; custom estimators remain authoritative
and their errors must not be swallowed.

The factory marks only its new request-local guidance message through
`WithBudgetInputGuidance`. Request identity, last-message pointer identity, and
a digest must match before excluding that message from prefix matching only.
User or tool prose is never recognized as guidance by wording. Copied requests,
mutated marks, and unmarked lookalikes fail closed.

The observation retains the entire measured prompt count, including previous
guidance. Current guidance is charged again at its full byte bound; no unknown
old token contribution is subtracted. Stored history contains only hashes and
counts behind a per-decorator mutex. Estimation alone neither learns nor spends.
Overflow must fail rather than replace a larger measured count with a smaller
byte count.

## Regression coverage

`TestBotSynthesisFactoryMeasuredPromptContinues` uses the factory model chain,
a real restored `RunBudget`, a metered deterministic provider, and the existing
progress-dispatch fixture. It activates `progress_v1` on the captured factory
binding after construction to test the generation branch independently of the
durable runner's context-restoration requirements.

The regression verifies:

- Provider dispatch and settlement occur for every generation, with generation
  health active and no whole-generation age deadline.
- Repeated same-prefix research continues despite updated budget guidance.
- Changed history remains conservative and switches to final synthesis sooner.
- Successive research calls spend from one ledger until genuine pressure starts
  final synthesis; the tools-disabled provider call returns a complete answer,
  rather than failing admission or resetting the budget.
- Input and output usage rise by exactly the provider-reported amounts, tool
  usage remains retained, and total usage never exceeds the original limit.

Run the focused regression with:

```sh
go test ./pkg/launcher -run '^TestBotSynthesisFactoryMeasuredPromptContinues$' -count=1
```

This is a deterministic model-chain regression, not a live-provider or durable
progress-store integration test.
