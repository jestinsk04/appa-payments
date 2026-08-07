package domains

const (
	R4CodeInProgress = "AC00"
	R4CodeInPending  = "11"
)

// IsR4BreakCode returns true if the code is one that indicates the payment is still in progress.
func IsR4BreakCode(code string) bool {
	return code == R4CodeInProgress || code == R4CodeInPending
}
