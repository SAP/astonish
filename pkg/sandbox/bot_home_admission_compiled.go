package sandbox

// BotHomeAdmissionMode selects the existing lifecycle wait or archive refusal
// behavior. It is a fixed internal protocol, not a model-callable operation.
type BotHomeAdmissionMode string

const (
	BotHomeAdmissionWait BotHomeAdmissionMode = "wait"
	BotHomeAdmissionTry  BotHomeAdmissionMode = "try"
)
