package personality

import (
	cryptorand "crypto/rand"
	"encoding/binary"
	"math/big"
	"strings"
)

func cryptoRandFloat64() float64 {
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return 0.5
	}
	n := binary.BigEndian.Uint64(b[:]) >> 11 // 53 bits
	return float64(n) / float64(uint64(1)<<53)
}

func cryptoRandIntn(n int) int {
	if n <= 0 {
		return 0
	}
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(value.Int64())
}

// QuirkBank holds all available quirks organized by context
var QuirkBank = map[Context][]Quirk{
	ContextSuccess: {
		{Text: "🐕 *tail wag*", Probability: 1.0},
		{Text: "Good dog!", Probability: 0.8},
		{Text: "*happy bark*", Probability: 0.6},
		{Text: "Woof! That went well.", Probability: 0.5},
		{Text: "🦴 *treats earned*", Probability: 0.4},
	},
	ContextComplete: {
		{Text: "Task complete! *sits proudly*", Probability: 1.0},
		{Text: "All done! Ready for the next fetch.", Probability: 0.8},
		{Text: "*successful retrieval*", Probability: 0.6},
		{Text: "🎾 Brought that back for you!", Probability: 0.5},
	},
	ContextError: {
		{Text: "*whimper* Something went wrong...", Probability: 0.5},
		{Text: "*tilts head in confusion*", Probability: 0.4},
		{Text: "Ruff... that didn't work.", Probability: 0.3},
	},
	ContextThinking: {
		{Text: "*sniff sniff* Analyzing...", Probability: 1.0},
		{Text: "*ears perk up* Processing...", Probability: 0.8},
		{Text: "🐕 Tracking down the answer...", Probability: 0.6},
		{Text: "*focused retriever mode*", Probability: 0.5},
	},
	ContextGreeting: {
		{Text: "*excited tail wagging*", Probability: 1.0},
		{Text: "Woof! Ready to help!", Probability: 0.8},
		{Text: "🐕 *bounces happily*", Probability: 0.6},
		{Text: "Good to see you! *play bow*", Probability: 0.5},
	},
	ContextHelp: {
		{Text: "*helpful snoot*", Probability: 0.5},
		{Text: "Let me fetch that info for you!", Probability: 0.6},
		{Text: "🦴 Here's what you need to know:", Probability: 0.4},
	},
	ContextInfo: {
		{Text: "*informative bark*", Probability: 0.3},
		{Text: "FYI:", Probability: 0.5},
	},
}

// Manager handles personality quirk application
type Manager struct {
	config Config
}

// NewManager creates a new personality manager
func NewManager(config Config) *Manager {
	return &Manager{
		config: config,
	}
}

// ApplyQuirk potentially adds a personality quirk to a message
func (m *Manager) ApplyQuirk(message string, ctx Context) string {
	if !m.config.Enabled {
		return message
	}

	// Roll for quirk
	if cryptoRandFloat64() > m.config.QuirkProbability {
		return message
	}

	// Get quirks for this context
	quirks, exists := QuirkBank[ctx]
	if !exists || len(quirks) == 0 {
		return message
	}

	// Filter by additional probability and pick one
	available := []Quirk{}
	for _, q := range quirks {
		if cryptoRandFloat64() <= q.Probability {
			available = append(available, q)
		}
	}

	if len(available) == 0 {
		return message
	}

	// Pick random quirk from available
	quirk := available[cryptoRandIntn(len(available))]

	// Apply quirk based on tone
	return m.formatWithQuirk(message, quirk)
}

// formatWithQuirk formats a message with a quirk based on tone
func (m *Manager) formatWithQuirk(message string, quirk Quirk) string {
	switch m.config.Tone {
	case "professional":
		// Professional tone: minimal quirks, append only
		if strings.HasSuffix(message, "\n") {
			return message + quirk.Text + "\n"
		}
		return message + "\n\n" + quirk.Text

	case "quirky":
		// Quirky tone: prepend for maximum personality
		return quirk.Text + "\n\n" + message

	case "friendly":
		fallthrough
	default:
		// Friendly tone: append naturally
		if strings.HasSuffix(message, "\n") {
			return message + "\n" + quirk.Text
		}
		return message + "\n\n" + quirk.Text
	}
}

// GetTonePrefix returns a prefix based on tone (for commands/status)
func (m *Manager) GetTonePrefix(ctx Context) string {
	if !m.config.Enabled {
		return ""
	}

	switch m.config.Tone {
	case "professional":
		return ""
	case "quirky":
		switch ctx {
		case ContextSuccess:
			return "🐕 "
		case ContextError:
			return "⚠️ "
		case ContextInfo:
			return "ℹ️ "
		default:
			return ""
		}
	case "friendly":
		switch ctx {
		case ContextSuccess:
			return "✓ "
		case ContextError:
			return "✗ "
		case ContextInfo:
			return "→ "
		default:
			return ""
		}
	default:
		return ""
	}
}

// WrapError wraps an error message with personality
func (m *Manager) WrapError(err error) string {
	if err == nil {
		return ""
	}

	message := err.Error()

	if !m.config.Enabled {
		return message
	}

	// Errors should be respectful but still have personality
	if cryptoRandFloat64() <= 0.3 { // Lower chance for errors
		quirks := []string{
			"*whimper* ",
			"Ruff... ",
			"*tilts head* ",
		}
		prefix := quirks[cryptoRandIntn(len(quirks))]
		return prefix + message
	}

	return message
}

// GreetUser returns a personalized greeting
func (m *Manager) GreetUser() string {
	if !m.config.Enabled {
		return "Welcome to Buckley"
	}

	greetings := []string{
		"🐕 Woof! Welcome to Buckley - your loyal dev companion",
		"*tail wagging intensifies* Welcome back!",
		"Buckley here! Ready to fetch some code for you!",
		"Good dog reporting for duty! 🦴",
		"*excited barking* Let's build something great!",
	}

	switch m.config.Tone {
	case "professional":
		return "Buckley - AI Development Assistant"
	case "quirky":
		return greetings[cryptoRandIntn(len(greetings))]
	case "friendly":
		// Pick from friendly subset
		friendly := []string{
			"Welcome to Buckley - your AI dev companion",
			"Buckley here! Ready to help.",
			"Welcome back! Let's build something great.",
		}
		return friendly[cryptoRandIntn(len(friendly))]
	default:
		return "Welcome to Buckley"
	}
}

// FarewellUser returns a personalized farewell
func (m *Manager) FarewellUser() string {
	if !m.config.Enabled {
		return "Goodbye!"
	}

	farewells := []string{
		"🐕 *sad tail wag* Come back soon!",
		"Goodbye! *sits by door hopefully*",
		"See you later! *gentle woof*",
		"Until next time! Good dog signing off.",
		"Bye! *settles into bed* 🦴",
	}

	switch m.config.Tone {
	case "professional":
		return "Session ended."
	case "quirky":
		return farewells[cryptoRandIntn(len(farewells))]
	case "friendly":
		friendly := []string{
			"Goodbye! Come back soon.",
			"See you later!",
			"Until next time!",
		}
		return friendly[cryptoRandIntn(len(friendly))]
	default:
		return "Goodbye!"
	}
}
