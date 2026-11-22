package connectivity

// PathLabel formats a negotiated path for display. Diagnostics use the original path.
func PathLabel(path string) string {
	switch path {
	case "P2P · QUIC":
		return "P2P · UDP"
	case "Relay · QUIC":
		return "Relay · UDP"
	default:
		return path
	}
}
