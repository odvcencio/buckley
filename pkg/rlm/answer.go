package rlm

// Answer represents the coordinator's evolving response state.
type Answer struct {
	Content    string
	Ready      bool
	Confidence float64
	Artifacts  []string
	NextSteps  []string
	Iteration  int
	TokensUsed int
}

// NewAnswer initializes an empty answer for an iteration.
func NewAnswer(iteration int) Answer {
	if iteration < 0 {
		iteration = 0
	}
	return Answer{Iteration: iteration}
}

// Normalize clamps fields into safe ranges.
func (a *Answer) Normalize() {
	a.Confidence = clampConfidence(a.Confidence)
	if a.Iteration < 0 {
		a.Iteration = 0
	}
	if a.TokensUsed < 0 {
		a.TokensUsed = 0
	}
}

func clampConfidence(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
