package tui

// RestoreLines restores a remote audit transcript without constructing a local
// conversation runtime. Queue ownership and model context remain server-side.
func (m *Model) RestoreLines(lines []string) {
	m.lines = append(m.lines, lines...)
	m.refreshViewport()
}
