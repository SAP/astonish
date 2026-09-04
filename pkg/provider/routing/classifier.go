package routing

// ComplexityScore is a float64 in [0, 1] where 0 = trivial (weak model)
// and 1 = complex (strong model).
type ComplexityScore float64

// ComplexityClassifier determines the complexity of a prompt for routing.
type ComplexityClassifier interface {
	Classify(prompt string, ctx ClassifierContext) ComplexityScore
}

// ClassifierContext provides additional signals for classification.
type ClassifierContext struct {
	ToolNames         []string
	ConversationTurns int
	HasPlanMode       bool
}
