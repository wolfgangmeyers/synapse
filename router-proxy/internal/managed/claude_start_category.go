package managed

import (
	"errors"
	"strings"
)

// claudeDefiniteStart keeps the complete cause on the identifying log line
// while preserving the joined error chain for classification and safety checks.
func claudeDefiniteStart(cause error) error {
	return &claudeSingleLineError{cause: errors.Join(errClaudeDefiniteStart, cause)}
}

type claudeSingleLineError struct{ cause error }

func (e *claudeSingleLineError) Error() string {
	return strings.NewReplacer("\r", `\r`, "\n", `\n`).Replace(e.cause.Error())
}
func (e *claudeSingleLineError) Unwrap() error { return e.cause }

// A category describes a failure, not whether replay is safe.
type claudeStartCategory string

const (
	claudeStartAuth        claudeStartCategory = "auth"
	claudeStartSpawn       claudeStartCategory = "spawn"
	claudeStartProbeDied   claudeStartCategory = "probe-died"
	claudeStartPoolFenced  claudeStartCategory = "pool-fenced"
	claudeStartGrantFailed claudeStartCategory = "grant-failed"
	claudeStartUnknown     claudeStartCategory = "unknown"
)

type claudeStartError struct {
	category claudeStartCategory
	cause    error
}

func (e *claudeStartError) Error() string { return e.cause.Error() }
func (e *claudeStartError) Unwrap() error { return e.cause }
func claudeStartCategorize(category claudeStartCategory, cause error) error {
	if cause == nil {
		return nil
	}
	return &claudeStartError{category: category, cause: cause}
}
func claudeStartCategoryOf(err error) claudeStartCategory {
	var e *claudeStartError
	if errors.As(err, &e) {
		switch e.category {
		case claudeStartAuth, claudeStartSpawn, claudeStartProbeDied, claudeStartPoolFenced, claudeStartGrantFailed:
			return e.category
		}
	}
	var fence *ClaudeAdmissionFencedError
	if errors.As(err, &fence) {
		return claudeStartPoolFenced
	}
	return claudeStartUnknown
}

var errClaudeExecutableMissing = errors.New("confirmed Claude executable resolution failure")

func claudeStartPromptSettlement(err error) bool {
	category := claudeStartCategoryOf(err)
	return category == claudeStartAuth || (category == claudeStartSpawn && errors.Is(err, errClaudeExecutableMissing))
}
