package slog

import (
	"runtime"
	"sync"
	"testing"
)

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
