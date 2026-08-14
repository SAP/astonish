package a2a

// AgentCardConfig holds configuration for building an Agent Card.
type AgentCardConfig struct {
	Name        string
	Description string
	BaseURL     string
	Version     string
	AuthMethods []string // e.g., ["bearer", "api_key"]
}

// BuildAgentCard constructs an Agent Card from configuration and skills.
func BuildAgentCard(cfg AgentCardConfig, skills []Skill) *AgentCard {
	card := &AgentCard{
		Name:        cfg.Name,
		Description: cfg.Description,
		URL:         cfg.BaseURL + "/api/a2a",
		Version:     cfg.Version,
		Provider: &AgentProvider{
			Organization: "Astonish",
			URL:          cfg.BaseURL,
		},
		Capabilities: &AgentCapabilities{
			Streaming:              true,
			PushNotifications:      true,
			StateTransitionHistory: true,
		},
		DefaultInputModes:  []string{"text/plain", "application/json"},
		DefaultOutputModes: []string{"text/plain", "text/markdown", "application/json"},
		Skills:             skills,
	}

	// Build security schemes from configured auth methods
	card.SecuritySchemes = make(map[string]SecurityScheme)
	card.Security = make([]map[string][]string, 0)

	for _, method := range cfg.AuthMethods {
		switch method {
		case "bearer":
			card.SecuritySchemes["bearerAuth"] = SecurityScheme{
				Type:   "http",
				Scheme: "bearer",
			}
			card.Security = append(card.Security, map[string][]string{"bearerAuth": {}})
		case "api_key":
			card.SecuritySchemes["apiKeyAuth"] = SecurityScheme{
				Type: "apiKey",
				In:   "header",
				Name: "X-API-Key",
			}
			card.Security = append(card.Security, map[string][]string{"apiKeyAuth": {}})
		}
	}

	if len(card.SecuritySchemes) == 0 {
		// Default: both bearer and API key
		card.SecuritySchemes["bearerAuth"] = SecurityScheme{Type: "http", Scheme: "bearer"}
		card.SecuritySchemes["apiKeyAuth"] = SecurityScheme{Type: "apiKey", In: "header", Name: "X-API-Key"}
		card.Security = []map[string][]string{
			{"bearerAuth": {}},
			{"apiKeyAuth": {}},
		}
	}

	return card
}
