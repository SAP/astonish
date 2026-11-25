# Shared Chat search collision and Bot research guard

## Contract

A resumed, owner-confirmed Bot title run using configured Perplexity must prepare the same scoped Chat catalog as Studio. Production must not install a second `bot_research` group containing `perplexity_web_search`: scoped Chat already owns that executable, and strict duplicate-name rejection remains intentional.

`pkg/daemon/bot_production.go` retains the research episode during production preparation. The launcher creates a private admission/episode guard, not another catalog entry. `botSharedDispatch.Dispatch` routes the original shared search through that guard and then calls the already budgeted shared executable.

## Boundaries and accounting

- Shared argument admission follows the actual `tools.PerplexityWebSearchArgs` schema plus the legacy typed facet. Omitted/null/zero result counts use the saved ceiling; positive counts use `min(requested, ceiling)`, including live requests for 8 and 6 under a ceiling of 5. Negative/fractional/wrong-type counts, unknown fields and trailing JSON reject before reservation or provider dispatch. Original invocation arguments and canonical digest remain unchanged; effective arguments are recomputed after serialization under current authority.
- Bounded recency (`day/week/month/year/recent`), mode (`general/academic/news/finance`) and detail (`concise/normal/detailed`) hints are forwarded. At most ten syntactically public hostname preferences are allowed; IP addresses, URLs, local/internal names and credential placeholders reject. Domain preferences do not authorize fetch origins or resource access. Existing query/facet checks are mechanical admission, not a semantic subject-security proof.
- Admission and route checks run again after waiting for episode serialization and before durable quota reservation.
- `botBoundTool` owns the single dispatch tool reservation and progress boundary. The episode independently reserves one durable public-research call. Chat's configured provider wrapper owns the model reservation. The continuation does not construct a second provider or reserve another tool call.
- The continuation passes the admitted query, bounded effective result count and validated public hints to Chat's concrete search. Successful sources update the episode, retaining search-before-detail behavior. A quota rejection emits terminal nonexecution evidence rather than implying provider success.
- MCP research still uses its existing confined alias; this change does not claim shared qualified MCP research parity.

## Regression evidence

`TestBotTitleConfiguredSearchHTTP` exercises authenticated creation, question-only initial execution, HTTP approve-and-continue, and resumed configured-search execution for personal and team owners. The populated Perplexity branches invoke the actual `newProductionBotWorker` Prepare closure and keep its returned catalog, episode, publication resources and isolation guard. Only external provider generation and retained files/lazy node infrastructure are fixtures; the local detail endpoint additionally retains the pre-existing confined read classifier (not another tool registration). The returned answer must include one configured search followed by one real local HTTP detail fetch and a successful fresh report. The unavailable-engine branches continue to verify an honest blocker with no search or detail dispatch.

`TestBotSharedResearchAdvertisedSchema` reads the real shared declaration and exercises authorized shared dispatch with all six advertised arguments, omitted optionals and null optionals through an injected deterministic configured provider. It asserts single research/model accounting, actual sourced output, hint normalization before provider generation, malicious/unknown/wrong-type rejection without provider or research-quota consumption, and live mandate revocation.

`TestBotSharedResearchContinuationCeilingAndDenial` checks two successful durable reservations and no further continuation after quota, revocation or route drift, plus foreign-session and invalid-result rejection. `TestBotSharedResearchToolErrorCorrection` exercises shared discovery, description and bridge dispatch, rejects a negative result count without provider dispatch, corrects to the live 8-result preference under the saved ceiling, and captures a sourced employee final without mandatory review. `TestChatRuntimeLazyNameCollisions` remains unchanged and continues rejecting actual duplicate executable names.

Reintroducing the old `bot_research` duplicate into the actual production-prepared catalog makes the resumed personal test fail with `scoped tool "perplexity_web_search" conflicts with another executable`; removing it passes personal/team populated and unavailable branches. The strict collision tests were not weakened.

This evidence is not a paid-provider, Docker launch, live daemon or Studio-browser acceptance claim.
