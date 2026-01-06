package personality

// Config represents personality system configuration
type Config struct {
	Enabled          bool
	QuirkProbability float64 // 0.0-1.0, probability of adding a quirk
	Tone             string  // "professional", "friendly", "quirky"
}

// DefaultConfig returns default personality settings
func DefaultConfig() Config {
	return Config{
		Enabled:          true,
		QuirkProbability: 0.15,
		Tone:             "friendly",
	}
}

// Context represents the context for a response
type Context string

const (
	ContextSuccess  Context = "success"  // Successful operation
	ContextError    Context = "error"    // Error occurred
	ContextInfo     Context = "info"     // Informational message
	ContextThinking Context = "thinking" // AI is processing
	ContextComplete Context = "complete" // Task completed
	ContextWaiting  Context = "waiting"  // Waiting for input
	ContextGreeting Context = "greeting" // Greeting user
	ContextHelp     Context = "help"     // Providing help
)

// Quirk represents a personality quirk that can be added to responses
type Quirk struct {
	Text        string
	Contexts    []Context // Which contexts this quirk applies to
	Probability float64   // Additional probability modifier
}

// PersonaDefinition describes a custom persona profile loaded from config.
type PersonaDefinition struct {
	Name        string            `yaml:"name"`
	Summary     string            `yaml:"summary"`
	Description string            `yaml:"description"`
	Traits      []string          `yaml:"traits"`
	Goals       []string          `yaml:"goals"`
	Directives  []string          `yaml:"directives"`
	Voice       map[string]string `yaml:"voice"`
	Style       PersonaStyle      `yaml:"style"`
}

// PersonaStyle controls tone and delivery preferences.
type PersonaStyle struct {
	Tone             string  `yaml:"tone"`
	QuirkProbability float64 `yaml:"quirk_probability"`
	ResponseLength   string  `yaml:"response_length"`
}

// PersonaProfile represents a runtime persona with defaults applied.
type PersonaProfile struct {
	ID string
	PersonaDefinition
}
