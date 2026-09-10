package types

import "testing"

func TestSSHStringRoundTrip(t *testing.T) {
	const value = "command longer than 255 bytes is not truncated: " +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	payload := MarshalSSHString(value)
	decoded, err := ParseSSHString(payload)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != value {
		t.Fatalf("decoded value mismatch: got %d bytes, want %d", len(decoded), len(value))
	}
}

func TestParseSSHStringRejectsShortPayload(t *testing.T) {
	for length := 0; length < 4; length++ {
		if _, err := ParseSSHString(make([]byte, length)); err == nil {
			t.Fatalf("accepted malformed payload with length %d", length)
		}
	}
}
