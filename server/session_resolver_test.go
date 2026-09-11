package server

import (
	"testing"

	"slider/pkg/interpreter"
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

func TestProcessInfoIsPreservedWithoutAffectingSessionKeys(t *testing.T) {
	srv := &server{unifiedSessionIDs: make(map[SessionKey]int64)}
	local := session.NewServerFromClientSession(
		slog.NewLogger("process-info-test"),
		nil,
		nil,
		nil,
		nil,
		"127.0.0.1",
		nil,
	)
	defer func() { _ = local.Close() }()
	local.SetPeerProcessInfo(interpreter.ProcessInfo{Name: "slider-client", PID: 111})

	localUnified := srv.createUnifiedFromLocal(local)
	if localUnified.ProcessInfo != local.GetPeerProcessInfo() {
		t.Fatalf("local process info = %#v, want %#v",
			localUnified.ProcessInfo, local.GetPeerProcessInfo())
	}

	entry := remoteSessionEntry{
		gatewayID: 10,
		rs: session.RemoteSession{
			ID:      21,
			Path:    []int64{11},
			Process: &interpreter.ProcessInfo{Name: "bad\nname", PID: 222},
		},
	}
	lookup := srv.buildRemoteLookup([]remoteSessionEntry{entry})
	remoteUnified := srv.createUnifiedFromRemote(entry, lookup)
	if remoteUnified.ProcessInfo.Name != "bad\uFFFDname" ||
		remoteUnified.ProcessInfo.PID != 222 {
		t.Fatalf("remote process info = %#v", remoteUnified.ProcessInfo)
	}

	changed := entry
	changed.rs.Process = &interpreter.ProcessInfo{Name: "different", PID: 333}
	changedLookup := srv.buildRemoteLookup([]remoteSessionEntry{changed})
	key := newSessionKey(entry.gatewayID, entry.rs.Path, entry.rs.ID)
	if lookup[key] != changedLookup[key] {
		t.Fatalf("process info changed stable session ID from %d to %d",
			lookup[key], changedLookup[key])
	}
}
