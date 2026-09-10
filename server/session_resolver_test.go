package server

import (
	"testing"

	"slider/pkg/session"
	"slider/pkg/slog"
)

func TestRemoteUnifiedIDsRemainStable(t *testing.T) {
	srv := &server{unifiedSessionIDs: make(map[SessionKey]int64)}
	first := remoteSessionEntry{
		gatewayID: 10,
		rs: session.RemoteSession{
			ID:   21,
			Path: []int64{11},
		},
	}
	second := remoteSessionEntry{
		gatewayID: 10,
		rs: session.RemoteSession{
			ID:   22,
			Path: []int64{11},
		},
	}

	initial := srv.buildRemoteLookup([]remoteSessionEntry{first, second})
	reordered := srv.buildRemoteLookup([]remoteSessionEntry{second, first})

	firstKey := newSessionKey(first.gatewayID, first.rs.Path, first.rs.ID)
	secondKey := newSessionKey(second.gatewayID, second.rs.Path, second.rs.ID)
	if initial[firstKey] != reordered[firstKey] {
		t.Fatalf("first session ID changed from %d to %d", initial[firstKey], reordered[firstKey])
	}
	if initial[secondKey] != reordered[secondKey] {
		t.Fatalf("second session ID changed from %d to %d", initial[secondKey], reordered[secondKey])
	}

	local := session.NewServerFromClientSession(
		slog.NewLogger("stable-id-test"),
		nil,
		nil,
		nil,
		nil,
		"127.0.0.1",
		nil,
	)
	defer func() { _ = local.Close() }()
	if local.GetID() == initial[firstKey] || local.GetID() == initial[secondKey] {
		t.Fatalf("local session reused a remote unified ID: %d", local.GetID())
	}
}

func TestRemoteStateKeyIncludesActualSessionID(t *testing.T) {
	first := UnifiedSession{Key: newSessionKey(10, nil, 21)}
	second := UnifiedSession{Key: newSessionKey(10, nil, 22)}

	if first.stateKey(remoteStateSSH) == second.stateKey(remoteStateSSH) {
		t.Fatal("sibling remote sessions share the same state key")
	}
	if first.stateKey(remoteStateSSH) == first.stateKey(remoteStatePortForward) {
		t.Fatal("SSH endpoint and port-forward state keys must remain distinct")
	}
}
