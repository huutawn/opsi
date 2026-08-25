package connection

import "errors"

const (
	ErrorUnsupportedProtocol  = "unsupported_protocol"
	ErrorUnsupportedSource    = "unsupported_source"
	ErrorInvalidTemplate      = "invalid_template"
	ErrorInvalidFact          = "invalid_fact"
	ErrorMissingFact          = "missing_fact"
	ErrorMissingCredential    = "missing_credential"
	ErrorUnrepresentableValue = "unrepresentable_value"
)

// CompileError is the stable, non-sensitive internal classification for a
// connection compilation failure. Messages deliberately describe only the
// rejected rule and never include templates or connection facts.
type CompileError struct {
	code    string
	message string
}

func (e CompileError) Error() string { return e.message }

func (e CompileError) Code() string { return e.code }

func compileError(code, message string) error {
	return CompileError{code: code, message: message}
}

func ErrorCode(err error) (string, bool) {
	var compileErr CompileError
	if !errors.As(err, &compileErr) {
		return "", false
	}
	return compileErr.Code(), true
}
