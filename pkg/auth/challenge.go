package auth

const ChallengeContext = "slider-web-auth-v1\x00"

// ChallengeMessage separates web authentication signatures from other protocols.
func ChallengeMessage(challenge []byte) []byte {
	message := make([]byte, 0, len(ChallengeContext)+len(challenge))
	message = append(message, ChallengeContext...)
	return append(message, challenge...)
}
