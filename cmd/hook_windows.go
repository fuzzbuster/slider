//go:build windows

package cmd

func monitorWindowResize(_ func(int, []byte) error, done <-chan struct{}) {
	// Windows doesn't support SIGWINCH in the same way.
	// We just wait for done channel to close.
	<-done
}
