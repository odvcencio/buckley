package skill

import "fmt"

// ErrSkillNotFound is returned when a skill cannot be found
type ErrSkillNotFound struct {
	Name string
}

func (e ErrSkillNotFound) Error() string {
	return fmt.Sprintf("skill not found: %s", e.Name)
}

// ErrInvalidSkill is returned when a skill file is malformed
type ErrInvalidSkill struct {
	Field  string
	Reason string
}

func (e ErrInvalidSkill) Error() string {
	return fmt.Sprintf("invalid skill: %s - %s", e.Field, e.Reason)
}

// ErrSkillAlreadyActive is returned when trying to activate an already active skill
type ErrSkillAlreadyActive struct {
	Name string
}

func (e ErrSkillAlreadyActive) Error() string {
	return fmt.Sprintf("skill already active: %s", e.Name)
}

// ErrSkillNotActive is returned when trying to deactivate an inactive skill
type ErrSkillNotActive struct {
	Name string
}

func (e ErrSkillNotActive) Error() string {
	return fmt.Sprintf("skill not active: %s", e.Name)
}
