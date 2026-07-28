package streaming

// Normalizer converts backend-specific output formats into standard E2B frames.
// Each Normalizer is bound to an execution ID that is stamped on outgoing frames.
type Normalizer struct {
	executionID string
}

// NewNormalizer creates a Normalizer that stamps frames with the given execution ID.
// If executionID is empty, frames are created without an execution ID.
func NewNormalizer(executionID string) *Normalizer {
	return &Normalizer{executionID: executionID}
}

// NormalizeStdout converts a raw stdout string into a StdoutData frame.
func (n *Normalizer) NormalizeStdout(content string) *Frame {
	return NewStdoutFrame(content, n.executionID)
}

// NormalizeStderr converts a raw stderr string into a StderrData frame.
func (n *Normalizer) NormalizeStderr(content string) *Frame {
	return NewStderrFrame(content, n.executionID)
}

// NormalizeExitCode converts an exit code and duration into a ResultData frame.
func (n *Normalizer) NormalizeExitCode(exitCode int, duration float64) *Frame {
	return NewResultFrame(exitCode, n.executionID, duration)
}

// NormalizeError converts an error code and message into an ErrorData frame.
func (n *Normalizer) NormalizeError(code, message string) *Frame {
	return NewErrorFrame(code, message)
}

// NormalizeTermData converts raw terminal output into a TermData frame.
func (n *Normalizer) NormalizeTermData(data string) *Frame {
	return NewTermDataFrame(data)
}
