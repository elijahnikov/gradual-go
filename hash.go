package gradual

// HashString computes a DJB-variant hash using 32-bit signed integer arithmetic.
// Must produce identical results to the TypeScript implementation.
func HashString(s string) int {
	var h int32
	for _, ch := range s {
		h = (h << 5) - h + int32(ch)
	}
	if h < 0 {
		return int(-h)
	}
	return int(h)
}
