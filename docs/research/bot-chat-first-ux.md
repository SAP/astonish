# Chat-first Bot interaction principles

Research date: 2026-11-06. Design recommendations, not implementation.

## Conclusion

Astonish should present a Bot as a persistent conversation with an ongoing responsibility, not a set of database records to configure. Continuity, independent work and scheduled follow-up distinguish a Bot from ordinary Chat without requiring a different primary interface.

This chat-first direction **supersedes** the previous panel-heavy UX recommendation: configuration stages, physical-home preparation, lifecycle machinery and permanent Thread/Runs/Goals/Schedule/Memory tabs must not be prerequisites to asking for help. Earlier implementation plans remain useful for backend safety requirements, not as authority for the default interaction. This document does not claim the new UX is implemented or that release acceptance has passed.

## Evidence and limits

These principles synthesize public product documentation, help pages, design explanations and supplied behavioral reference material. This is a documented-interaction study, **not a hands-on visual audit** or proof of reliability. No signed-in reference account was exercised. Screenshots and walkthrough links were located, but their pixels and video could not be reliably reviewed. Published descriptions can differ by version or surface; conflicting permission behavior is not treated as settled evidence.

The supplied Astonish specification is a draft, not evidence of another interface. The supplied behavioral reference's provenance is **not independently verified**, and a prompt alone cannot establish how a running product behaves. No raw private prompt was submitted to web research. This neutral synthesis intentionally omits source identities, quotations, external citations and temporary research locations; it cannot serve as an independently reproducible source audit. Recommendations below require an Astonish prototype and measured acceptance, not an inference of feature parity.

## Interaction principles

### A named conversation first

Creation should open a conversation immediately, with at most a lightweight name step. The user describes the responsibility, sources, boundaries and first task. Ask only consequential missing questions; do not require a recurring schedule before the Bot can be useful.

The existing Chat layout should retain separate Bots and Chats sidebar sections. Selecting a Bot should show its identity/status, transcript, composer and existing artifact harness. Name, picture and details are secondary affordances. Memory should be correctable conversationally, with inspection available on demand.

### Persistent responsibility, not a mandatory dashboard

A Bot should preserve approved responsibility and preferences across turns. Conversational intake must really persist reviewed state: hiding forms without a conversation-to-state path does not satisfy this design. Existing configuration supplies model and storage defaults; an actual missing prerequisite should interrupt with a specific remedy, not a generic setup checklist.

Users should be able to follow up without navigating to a read-only execution page. Background work should remain visible through concise status and attention indicators, with activity and execution history accessible on demand. Closing a client does not mean stopping a running worker; shutting down the only worker cannot leave it executing. Communicate deployment limits honestly.

### Schedules through conversation, controls when needed

Separate what work does from when it repeats. Do a task once before proposing recurrence when useful. A request to repeat it should produce an in-thread confirmation stating work, cadence, timezone, sources, output and boundaries. Creation of recurrence must not silently run a task immediately.

Upcoming work and recent history belong behind a small schedule/details affordance. Pause, resume, test and delete need deterministic controls with clear scopes; edit can start an identified conversational change. Hiding a Bot, resetting a conversation, forgetting memory and deleting the Bot are different operations. Resuming a Bot must not imply consent to re-enable disabled recurrence.

### Results and bounded attention

Messages, files, questions and approval requests belong in the conversation, including results produced while the user was away. Reuse Astonish's existing output harness and folded tool details rather than creating a separate reporting UI. Unchanged monitoring can remain quiet; source failure must not become no-change, and explicitly requested reminders or promised digests must not be suppressed as uninteresting.

### Consent cannot be inferred

Conversation is not sufficient for every action. Present the exact proposed consequential action inline and require explicit approval bound only to that action. Preserve the binding during checkpoint continuation without forcing users to operate checkpoint mechanics. Do not introduce broad auto-approval or copy unverified permission scopes. Secure credential entry and storage remain dedicated workflows.

### Details on demand

A Bot-name or details control can reveal schedules, activity, remembered context and advanced settings. Keep auditability and direct controls without turning storage categories into primary navigation. Empty conversations should invite a responsibility or task; missing prerequisites and errors should identify a remedy without losing context; populated conversations should prioritize results and follow-up. Review a small interaction prototype before finalizing secondary navigation.

## Boundaries worth retaining

Keep tenant isolation, retained-home separation, action authorization, scoped credentials, budget enforcement and durable audit records. A simpler interface does not justify importing another system's security assumptions, connector/training defaults or cross-Bot coordination. Ordinary Chat, report rendering, terminal behavior and provider configuration are not changed by this research.

## Illustrative target interaction

This is a proposed Astonish interaction, not an observed transcript:

1. The user opens New Bot and asks it to monitor selected project releases.
2. The Bot asks for missing source details and performs the authorized initial review in the same conversation.
3. The user asks for weekday checks at nine, with important changes highlighted.
4. The Bot presents a compact recurrence confirmation including timezone and notification preference.
5. Later, a relevant update arrives in that conversation, with linked evidence and an artifact if useful.
6. The user requests twice-weekly checks; the Bot proposes the updated timing in context.
7. Schedule details are opened only to inspect upcoming work or use an explicit pause control.

The database may store many entities behind every step. The user should experience one continuing assistant that remembers and follows through. None of this sequence is established as implemented or live-verified by this document.
