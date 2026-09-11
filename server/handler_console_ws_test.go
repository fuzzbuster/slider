package server

import (
	"bytes"
	"os"
	"sync/atomic"
	"testing"

	"slider/pkg/types"

	"golang.org/x/term"
)

func TestResizeUsesCurrentWebConsole(t *testing.T) {
	ptyFile, err := os.CreateTemp(t.TempDir(), "pty")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ptyFile.Close() }()

	oldBuffer := new(bytes.Buffer)
	oldConsole := &Console{
		Term:       term.NewTerminal(oldBuffer, ""),
		ResizeChan: make(chan types.TermDimensions, 1),
	}
	currentBuffer := new(bytes.Buffer)
	currentConsole := &Console{
		Term:       term.NewTerminal(currentBuffer, ""),
		ResizeChan: make(chan types.TermDimensions, 1),
	}

	var current atomic.Pointer[Console]
	current.Store(oldConsole)
	current.Store(currentConsole)

	message := []byte(`{"type":"resize","cols":120,"rows":40}`)
	if !handleCurrentConsoleResize(message, ptyFile, &current) {
		t.Fatal("resize message was not handled")
	}

	select {
	case size := <-currentConsole.ResizeChan:
		if size.Width != 120 || size.Height != 40 {
			t.Fatalf("current console received size %dx%d", size.Width, size.Height)
		}
	default:
		t.Fatal("current console did not receive resize")
	}
	select {
	case <-oldConsole.ResizeChan:
		t.Fatal("old console received resize")
	default:
	}
}
