package slog

import (
	"encoding/json"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestJSONCallerOutputOmitsTextDelimiters(t *testing.T) {
	logger := NewLogger("unit")
	logger.LogToBuffer()
	logger.WithJSON(true)
	logger.WithCallerInfo(true)

	logger.Infof("hello %s", "world")

	output := captureStdout(t, logger.BufferOut)
	var entry map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &entry); err != nil {
		t.Fatalf("unmarshal JSON log: %v\noutput: %q", err, output)
	}

	caller, ok := entry["caller"].(string)
	if !ok {
		t.Fatalf("caller field = %v, want string", entry["caller"])
	}
	if strings.ContainsAny(caller, "<>") {
		t.Fatalf("caller field %q contains text delimiters", caller)
	}
	if !strings.Contains(caller, "slog_test.go:") {
		t.Fatalf("caller field = %q, want test file location", caller)
	}
}

func TestInfoWithTextOutputFormatsFields(t *testing.T) {
	logger := NewLogger("")
	logger.LogToBuffer()

	logger.InfoWith("saved", F("id", 42), F("name", "alice"))

	output := captureStdout(t, logger.BufferOut)
	if !strings.Contains(output, ` INFO saved id=42 name="alice"`) {
		t.Fatalf("output = %q, want formatted text fields", output)
	}
}

func TestSetLevelInvalidValueErrorText(t *testing.T) {
	err := NewLogger("").SetLevel("verbose")
	if err == nil {
		t.Fatal("SetLevel returned nil, want invalid-level error")
	}

	const want = "expected one of [debug|info|warn|error|off]"
	if got := err.Error(); got != want {
		t.Fatalf("SetLevel error = %q, want %q", got, want)
	}
}

func TestLogBufferConcurrentWriteAndDrain(t *testing.T) {
	buffer := &LogBuff{}
	const (
		writers = 8
		writes  = 1000
	)

	done := make(chan struct{})
	drained := make(chan int, 1)
	go func() {
		total := 0
		for {
			select {
			case <-done:
				total += len(buffer.drain())
				drained <- total
				return
			default:
				total += len(buffer.drain())
				runtime.Gosched()
			}
		}
	}()

	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range writes {
				_, _ = buffer.Write([]byte{'x'})
			}
		}()
	}
	wg.Wait()
	close(done)

	if got, want := <-drained, writers*writes; got != want {
		t.Fatalf("drained %d bytes, want %d", got, want)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	defer reader.Close()
	defer writer.Close()

	oldStdout := os.Stdout
	os.Stdout = writer
	defer func() {
		os.Stdout = oldStdout
	}()

	fn()

	if err := writer.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}

	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return string(output)
}
