package config

import (
	"fmt"
	"strings"
)

// Error is one validation failure.
type Error struct {
	Line int
	Text string
	Msg  string
	Hint string
}

func (e Error) Error() string {
	return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
}

// Errors is a non-empty collection of configuration validation failures.
type Errors []Error

func (e Errors) Error() string {
	if len(e) == 0 {
		return "config: no errors"
	}
	if len(e) == 1 {
		return fmt.Sprintf("config: 1 error: %s", e[0].Error())
	}
	messages := make([]string, len(e))
	for i, failure := range e {
		messages[i] = failure.Error()
	}
	return fmt.Sprintf("config: %d errors: %s", len(e), strings.Join(messages, "; "))
}
