package server

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"slider/pkg/auth"
	"slider/pkg/scrypt"
	"slider/pkg/slog"
)

func newAuthTestServer(t *testing.T) (*server, *scrypt.KeyPair) {
	t.Helper()

	serverKeyPair, err := scrypt.NewServerKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	clientKeyPair, err := scrypt.NewEd25519KeyPair()
	if err != nil {
		t.Fatal(err)
	}

	return &server{
		Logger:               slog.NewLogger("auth-test"),
		CertificateAuthority: serverKeyPair.CertificateAuthority,
		certTrack: &scrypt.CertTrack{
			Certs: map[int64]*scrypt.KeyPair{1: clientKeyPair},
		},
		authChallenges: make(map[string]authChallenge),
	}, clientKeyPair
}

func performJSONRequest(t *testing.T, handler http.HandlerFunc, body any) *httptest.ResponseRecorder {
	t.Helper()

	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(data))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestAuthRequiresPrivateKeyProof(t *testing.T) {
	s, keyPair := newAuthTestServer(t)
	s.fingerprint = "public-server-fingerprint"

	serverFingerprint := performJSONRequest(t, s.handleAuthChallenge, ChallengeRequest{
		Fingerprint: s.fingerprint,
	})
	if serverFingerprint.Code != http.StatusUnauthorized {
		t.Fatalf("server fingerprint challenge returned %d", serverFingerprint.Code)
	}

	fingerprintOnly := performJSONRequest(t, s.handleAuthToken, ChallengeRequest{
		Fingerprint: keyPair.FingerPrint,
	})
	if fingerprintOnly.Code != http.StatusBadRequest {
		t.Fatalf("fingerprint-only authentication returned %d", fingerprintOnly.Code)
	}

	challengeRecorder := performJSONRequest(t, s.handleAuthChallenge, ChallengeRequest{
		Fingerprint: keyPair.FingerPrint,
	})
	if challengeRecorder.Code != http.StatusOK {
		t.Fatalf("challenge request returned %d: %s", challengeRecorder.Code, challengeRecorder.Body.String())
	}

	var challenge ChallengeResponse
	if err := json.NewDecoder(challengeRecorder.Body).Decode(&challenge); err != nil {
		t.Fatal(err)
	}
	challengeBytes, err := base64.RawStdEncoding.DecodeString(challenge.Challenge)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := scrypt.SignerFromKey(keyPair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := signer.Sign(rand.Reader, auth.ChallengeMessage(challengeBytes))
	if err != nil {
		t.Fatal(err)
	}

	tokenRequest := TokenRequest{
		Fingerprint: keyPair.FingerPrint,
		ChallengeID: challenge.ChallengeID,
		Signature:   base64.RawStdEncoding.EncodeToString(signature.Blob),
	}
	tokenRecorder := performJSONRequest(t, s.handleAuthToken, tokenRequest)
	if tokenRecorder.Code != http.StatusOK {
		t.Fatalf("token request returned %d: %s", tokenRecorder.Code, tokenRecorder.Body.String())
	}

	var tokenResponse TokenResponse
	if err := json.NewDecoder(tokenRecorder.Body).Decode(&tokenResponse); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.validateToken(tokenResponse.Token); err != nil {
		t.Fatalf("issued token did not validate: %v", err)
	}

	replayRecorder := performJSONRequest(t, s.handleAuthToken, tokenRequest)
	if replayRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("challenge replay returned %d", replayRecorder.Code)
	}
}
