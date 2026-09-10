package auth

import (
	"bytes"
	"testing"
)

func TestChallengeMessageAddsDomainSeparation(t *testing.T) {
	challenge := []byte("challenge")
	message := ChallengeMessage(challenge)

	if !bytes.HasPrefix(message, []byte(ChallengeContext)) {
		t.Fatal("challenge context prefix is missing")
	}
	if !bytes.Equal(message[len(ChallengeContext):], challenge) {
		t.Fatal("challenge payload changed")
	}
}
