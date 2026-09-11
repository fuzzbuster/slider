package socks

import (
	"bytes"
	"io"
	"testing"

	"slider/pkg/types"
)

type fragmentedReadWriter struct {
	input  []byte
	output bytes.Buffer
}

func (rw *fragmentedReadWriter) Read(p []byte) (int, error) {
	if len(rw.input) == 0 {
		return 0, io.EOF
	}
	p[0] = rw.input[0]
	rw.input = rw.input[1:]
	return 1, nil
}

func (rw *fragmentedReadWriter) Write(p []byte) (int, error) {
	return rw.output.Write(p)
}

func TestPerformHandshakeSupportsFragmentedResponses(t *testing.T) {
	stream := &fragmentedReadWriter{
		input: []byte{
			0x05, 0x00,
			0x05, 0x00, 0x00, 0x01,
			127, 0, 0, 1, 0x1f, 0x90,
		},
	}
	client := &Client{}
	if err := client.performHandshake(stream, types.TcpIpChannelMsg{
		DstHost: "example.com",
		DstPort: 443,
	}); err != nil {
		t.Fatal(err)
	}
}
