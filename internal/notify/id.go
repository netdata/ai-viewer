package notify

import "strconv"

// formatID renders a hub event counter as a decimal string for the SSE
// "id:" field.
func formatID(n uint64) string {
	return strconv.FormatUint(n, 10)
}

// parseID parses a decimal event id back to its numeric value. ok is
// false for ids the hub did not mint (non-numeric, negative, or
// overflowing), which the caller treats as an uncoverable Last-Event-ID
// (resync). Numeric comparison — not lexical — is required because event
// ids are unpadded decimals where "10" must order after "9".
func parseID(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
