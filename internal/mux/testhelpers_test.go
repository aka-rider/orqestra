//go:build darwin

package mux

import "os"

// pipePair creates an os.File pipe pair for testing (reader, writer).
func pipePair() (*os.File, *os.File, error) {
	r, w, err := os.Pipe()
	return r, w, err
}

// setReadyForTest sets the mux tty and signals ready for test usage
// (bypasses the need to call Run).
func setReadyForTest(m *Mux, tty *os.File) {
	m.tty = tty
	select {
	case <-m.ready:
		// Already closed.
	default:
		close(m.ready)
	}
}
