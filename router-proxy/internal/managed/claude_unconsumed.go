package managed

import (
	"errors"
)

// ErrClaudeUnconsumed indicates that Claude failed before echoing/admitting the user input,
// leaving the message unconsumed and eligible for immediate retry.
var ErrClaudeUnconsumed = errors.New("Claude input was not consumed before process failure")
