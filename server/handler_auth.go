package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"slider/pkg/auth"
	"slider/pkg/scrypt"
	"slider/pkg/slog"

	"golang.org/x/crypto/ssh"
)

const (
	DefaultTokenLifetime  = 24 * time.Hour
	authChallengeLifetime = 2 * time.Minute
	maxAuthChallenges     = 1024
	maxAuthRequestSize    = 16 << 10
)

type TokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	TokenType string `json:"token_type"`
}

type ErrorResponse struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

type ChallengeRequest struct {
	Fingerprint string `json:"fingerprint"`
}

type ChallengeResponse struct {
	ChallengeID string `json:"challenge_id"`
	Challenge   string `json:"challenge"`
	ExpiresAt   string `json:"expires_at"`
}

type TokenRequest struct {
	Fingerprint string `json:"fingerprint"`
	ChallengeID string `json:"challenge_id"`
	Signature   string `json:"signature"`
}

type authChallenge struct {
	fingerprint string
	challenge   []byte
	expiresAt   time.Time
}

func (s *server) handleAuthChallenge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAuthRequestSize)
	var request ChallengeRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Fingerprint == "" {
		sendErrorJSON(w, http.StatusBadRequest, "invalid_request", "Missing fingerprint")
		return
	}

	if _, _, ok := s.getCertByFingerprint(request.Fingerprint); !ok {
		sendErrorJSON(w, http.StatusUnauthorized, "invalid_fingerprint", "Certificate fingerprint not found")
		return
	}

	challenge := make([]byte, 32)
	if _, err := rand.Read(challenge); err != nil {
		sendErrorJSON(w, http.StatusInternalServerError, "server_error", "Failed to create challenge")
		return
	}
	challengeIDBytes := make([]byte, 24)
	if _, err := rand.Read(challengeIDBytes); err != nil {
		sendErrorJSON(w, http.StatusInternalServerError, "server_error", "Failed to create challenge")
		return
	}

	now := time.Now()
	expiresAt := now.Add(authChallengeLifetime)
	challengeID := base64.RawURLEncoding.EncodeToString(challengeIDBytes)

	s.authChallengeMutex.Lock()
	if s.authChallenges == nil {
		s.authChallenges = make(map[string]authChallenge)
	}
	for id, pending := range s.authChallenges {
		if !pending.expiresAt.After(now) {
			delete(s.authChallenges, id)
		}
	}
	if len(s.authChallenges) >= maxAuthChallenges {
		s.authChallengeMutex.Unlock()
		sendErrorJSON(w, http.StatusServiceUnavailable, "server_busy", "Too many pending authentication attempts")
		return
	}
	s.authChallenges[challengeID] = authChallenge{
		fingerprint: request.Fingerprint,
		challenge:   challenge,
		expiresAt:   expiresAt,
	}
	s.authChallengeMutex.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ChallengeResponse{
		ChallengeID: challengeID,
		Challenge:   base64.RawStdEncoding.EncodeToString(challenge),
		ExpiresAt:   expiresAt.Format(time.RFC3339),
	})
}

// handleAuthToken verifies proof of private-key possession and issues a JWT.
func (s *server) handleAuthToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxAuthRequestSize)
	var request TokenRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil ||
		request.Fingerprint == "" || request.ChallengeID == "" || request.Signature == "" {
		sendErrorJSON(w, http.StatusBadRequest, "invalid_request",
			"Fingerprint, challenge ID and signature are required")
		return
	}

	s.authChallengeMutex.Lock()
	pending, ok := s.authChallenges[request.ChallengeID]
	delete(s.authChallenges, request.ChallengeID)
	s.authChallengeMutex.Unlock()
	if !ok || pending.fingerprint != request.Fingerprint || !pending.expiresAt.After(time.Now()) {
		sendErrorJSON(w, http.StatusUnauthorized, "invalid_challenge", "Challenge is invalid or expired")
		return
	}

	certID, keyPair, ok := s.getCertByFingerprint(request.Fingerprint)
	if !ok {
		sendErrorJSON(w, http.StatusUnauthorized, "invalid_fingerprint", "Certificate fingerprint not found")
		return
	}

	signature, err := base64.RawStdEncoding.DecodeString(request.Signature)
	if err != nil {
		sendErrorJSON(w, http.StatusBadRequest, "invalid_signature", "Signature encoding is invalid")
		return
	}
	signer, err := scrypt.SignerFromKey(keyPair.PrivateKey)
	if err != nil {
		s.ErrorWith("Failed to load authentication key", slog.F("cert_id", certID), slog.F("err", err))
		sendErrorJSON(w, http.StatusInternalServerError, "server_error", "Failed to validate signature")
		return
	}
	if err := signer.PublicKey().Verify(auth.ChallengeMessage(pending.challenge), &ssh.Signature{
		Format: signer.PublicKey().Type(),
		Blob:   signature,
	}); err != nil {
		sendErrorJSON(w, http.StatusUnauthorized, "invalid_signature", "Signature verification failed")
		return
	}

	claims := auth.NewClaims("slider-server", request.Fingerprint, certID, DefaultTokenLifetime)
	token, err := auth.Encode(claims, s.getJWTSecret())
	if err != nil {
		s.ErrorWith("Failed to encode JWT token",
			slog.F("fingerprint", request.Fingerprint),
			slog.F("cert_id", certID),
			slog.F("err", err))
		sendErrorJSON(w, http.StatusInternalServerError, "server_error", "Failed to generate token")
		return
	}

	s.DebugWith("Issued JWT token",
		slog.F("remote_addr", r.RemoteAddr),
		slog.F("fingerprint", request.Fingerprint),
		slog.F("cert_id", certID))

	http.SetCookie(w, &http.Cookie{
		Name:     SliderTokenCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(DefaultTokenLifetime.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(TokenResponse{
		Token:     token,
		ExpiresAt: time.Unix(claims.ExpiresAt, 0).Format(time.RFC3339),
		TokenType: "Bearer",
	})
}

func (s *server) getJWTSecret() []byte {
	return auth.DeriveSecret(s.CertificateAuthority.CAPrivateKey, "slider-jwt-v1")
}

func sendErrorJSON(w http.ResponseWriter, statusCode int, errorCode, description string) {
	response := ErrorResponse{
		Error:            errorCode,
		ErrorDescription: description,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(response)
}

// validateToken validates a JWT and confirms its certificate is still authorized.
func (s *server) validateToken(token string) (string, int64, error) {
	claims, err := auth.Decode(token, s.getJWTSecret())
	if err != nil {
		return "", 0, err
	}

	keyPair, err := s.getCert(claims.CertID)
	if err != nil || keyPair.FingerPrint != claims.Subject {
		return "", 0, fmt.Errorf("certificate is no longer authorized")
	}

	return claims.Subject, claims.CertID, nil
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     SliderTokenCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})

	s.DebugWith("User logged out", slog.F("remote_addr", r.RemoteAddr))

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("Logged out successfully"))
}
