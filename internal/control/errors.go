package control

import (
	"errors"
	"github.com/monkeyWie/reignhub/internal/terminal"
)

const WriteOutcomeUnknown = "WRITE_OUTCOME_UNKNOWN"

// ResponseError preserves machine-readable failure semantics across JSON APIs.
type ResponseError struct {
	Code    string
	Message string
}

func (e *ResponseError) ResponseCode() string { return e.Code }

func (e *ResponseError) Error() string        { return e.Message }
func (e *ResponseError) OutcomeUnknown() bool { return e.Code == WriteOutcomeUnknown }

func OutcomeUnknown(err error) bool {
	var outcome interface{ OutcomeUnknown() bool }
	return errors.As(err, &outcome) && outcome.OutcomeUnknown()
}

func ErrorCode(err error) string {
	if errors.Is(err, terminal.ErrMissing) {
		return "TERMINAL_GONE"
	}
	if errors.Is(err, terminal.ErrDetached) {
		return "TERMINAL_DETACHED"
	}
	if errors.Is(err, terminal.ErrClosing) {
		return "TERMINAL_CLOSING"
	}
	if errors.Is(err, terminal.ErrCloseUnconfirmed) {
		return "TERMINAL_CLOSE_UNCONFIRMED"
	}
	if OutcomeUnknown(err) {
		return WriteOutcomeUnknown
	}
	var response *ResponseError
	if errors.As(err, &response) {
		return response.Code
	}
	return ""
}
