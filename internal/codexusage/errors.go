package codexusage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/SilkageNet/codex-switch/internal/authschema"
)

const genericQueryError = "account usage query failed (diagnostics redacted)"
const maxPublicError = 1024

// publicError preserves error identity without exposing its untrusted cause
// through Error or Unwrap. Formatting and persistence use only the safe message.
type publicError struct {
	message string
	cause   error
}

func (err *publicError) Error() string        { return err.message }
func (err *publicError) Is(target error) bool { return errors.Is(err.cause, target) }
func (err *publicError) Format(state fmt.State, verb rune) {
	if verb == 'q' {
		_, _ = fmt.Fprintf(state, "%q", err.message)
		return
	}
	_, _ = io.WriteString(state, err.message)
}

var safeFailurePattern = regexp.MustCompile(`^(initialize|initialized|account/read|account/rateLimits/read|account/usage/read|Codex app server|account usage query|refreshed credentials|temporary CODEX_HOME): (protocol failure|request failed|invalid response|process failure|credential validation failed|I/O failure|cleanup failed|cancelled|deadline exceeded|unavailable) \(diagnostics redacted\)$`)
var safeRPCPattern = regexp.MustCompile(`^(initialize|account/read|account/rateLimits/read|account/usage/read) failed \(RPC code -?[0-9]{1,5}, diagnostics redacted\)$`)
var retryPrefix = regexp.MustCompile(`^query failed after [1-9][0-9]{0,2} attempts: `)

var safeMessages = map[string]bool{
	genericQueryError: true,
	"another codex-switch operation is already running":                     true,
	"temporary connection failure":                                          true,
	"network unavailable":                                                   true,
	"profile was removed while usage was being queried":                     true,
	"refreshed credentials are invalid":                                     true,
	"saved credentials became invalid":                                      true,
	"refreshed credentials do not match the saved account or workspace":     true,
	"refreshed token generations are ambiguous (reauthentication required)": true,
	"refreshed credentials are older than the validated generation":         true,
	"active credential reconciliation failed (diagnostics redacted)":        true,
	"usage cache is invalid":                                                true,
	"read usage cache failed":                                               true,
	"write usage cache failed":                                              true,
	"encode usage cache failed":                                             true,
	"usage cache update failed after credential reconciliation":             true,
}

func safeOperation(operation string) string {
	switch operation {
	case "initialize", "initialized", "account/read", "account/rateLimits/read", "account/usage/read", "Codex app server", "account usage query", "refreshed credentials", "temporary CODEX_HOME":
		return operation
	default:
		return "account usage query"
	}
}

func failure(operation, classification string, cause error) error {
	message := fmt.Sprintf("%s: %s (diagnostics redacted)", safeOperation(operation), classification)
	return &publicError{message: NormalizeErrorMessage(message), cause: cause}
}

// NormalizeErrorMessage accepts only the bounded grammar emitted by this
// package and fixed local classifications. Legacy or untrusted text is dropped.
func NormalizeErrorMessage(message string) string {
	if message == "" {
		return ""
	}
	if len(message) > maxPublicError {
		return genericQueryError
	}
	body := retryPrefix.ReplaceAllString(message, "")
	parts := strings.Split(body, "; ")
	if len(parts) > 4 {
		return genericQueryError
	}
	for _, part := range parts {
		if !safeMessages[part] && !safeFailurePattern.MatchString(part) && !safeRPCPattern.MatchString(part) {
			return genericQueryError
		}
	}
	return message
}

func ErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var parts []string
		for _, cause := range joined.Unwrap() {
			parts = append(parts, ErrorMessage(cause))
		}
		return NormalizeErrorMessage(strings.Join(parts, "; "))
	}
	var safe *publicError
	if errors.As(err, &safe) {
		return safe.message
	}
	if errors.Is(err, authschema.ErrAmbiguousGeneration) {
		return "refreshed token generations are ambiguous (reauthentication required)"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "account usage query: deadline exceeded (diagnostics redacted)"
	}
	if errors.Is(err, context.Canceled) {
		return "account usage query: cancelled (diagnostics redacted)"
	}
	message := NormalizeErrorMessage(err.Error())
	if message == "" {
		return genericQueryError
	}
	return message
}

func RedactError(err error) error {
	if err == nil {
		return nil
	}
	return &publicError{message: ErrorMessage(err), cause: err}
}

func RetryError(attempts int, err error) error {
	message := fmt.Sprintf("query failed after %d attempts: %s", attempts, ErrorMessage(err))
	return &publicError{message: NormalizeErrorMessage(message), cause: err}
}
