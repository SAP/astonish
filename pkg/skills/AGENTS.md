# pkg/skills — AGENTS.md

`SKILL.md` loader, validator, and [ClawHub](https://clawhub.com) integration. Skills teach the agent about tools, CLIs, and workflows via markdown files.

## Scope
- `loader.go` — `Skill`, `ParseSkillFile`, `IsEligible`.
- `validator.go` — `ValidationResult`, `ValidationIssue`, `ValidatorConfig`.
- `clawhub.go` — `ClawHubMeta`, `InstallResult`.

## Key rules
1. **A skill is a `SKILL.md` file** — no code, no hidden files. The whole contract is markdown parsing + eligibility rules.
2. **Eligibility is deterministic**: `IsEligible` must return the same result for the same session state. Do not add nondeterministic checks.
3. **Scoped cascade**: platform, org, and team definitions come through scoped stores; preserve cascade order and same-name overrides. Never leak a private/personal skill into shared scope.
4. **ClawHub metadata is normalized before use** — trust the normalized shape, not raw metadata.
5. Treat downloaded skill content as untrusted input: validate paths and metadata before installation, and never execute install-time content implicitly.

## When editing
- Adding a front-matter field? Extend `Skill`, update validation and serialization, and document the field.
- Changing eligibility or prompt-visible loading? Coordinate with `pkg/agent` because selected skills enter the system prompt.
- Changing scoped persistence? Follow [`pkg/store/AGENTS.md`](../store/AGENTS.md).

## Verification

Run `go test ./pkg/skills/...`; include `./pkg/agent/...` when changing prompt-visible loading or eligibility.
