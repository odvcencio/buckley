package personality

// ConfigFromYAML converts YAML config structure to personality Config
func ConfigFromYAML(enabled bool, quirkProbability float64, tone string) Config {
	if tone == "" {
		tone = "friendly"
	}

	// Validate tone
	validTones := map[string]bool{
		"professional": true,
		"friendly":     true,
		"quirky":       true,
	}

	if !validTones[tone] {
		tone = "friendly"
	}

	// Clamp probability to 0-1 range
	if quirkProbability < 0 {
		quirkProbability = 0
	}
	if quirkProbability > 1 {
		quirkProbability = 1
	}

	return Config{
		Enabled:          enabled,
		QuirkProbability: quirkProbability,
		Tone:             tone,
	}
}
