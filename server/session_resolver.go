package server

import (
	"fmt"
	"strconv"
	"strings"

	"slider/pkg/interpreter"
	"slider/pkg/session"
)

type SessionKey struct {
	GatewayID int64
	Path      string
	ActualID  int64
}

// UnifiedSession represents a normalized session (local or remote).
type UnifiedSession struct {
	UnifiedID int64
	ActualID  int64
	OwnerID   int64
	GatewayID int64
	Key       SessionKey
	Role      string

	BaseInfo   interpreter.BaseInfo
	WorkingDir string

	IsConnector    bool
	IsGateway      bool
	ConnectionAddr string
	Path           []int64
}

type remoteStateKind uint8

const (
	remoteStateSSH remoteStateKind = iota
	remoteStateShell
	remoteStateSocks
	remoteStatePortForward
)

type remoteStateKey struct {
	Session SessionKey
	Kind    remoteStateKind
}

type remoteSessionEntry struct {
	rs             session.RemoteSession
	gatewayID      int64
	gatewayUnified int64
}

func (s *server) ResolveUnifiedSessions() map[int64]UnifiedSession {
	unifiedMap := make(map[int64]UnifiedSession)
	localSessions := s.GetAllSessions()
	s.collectLocalSessions(unifiedMap, localSessions)
	remoteEntries := s.collectRemoteSessions(localSessions)
	remoteUnifiedLookup := s.buildRemoteLookup(remoteEntries)
	s.processRemoteSessions(unifiedMap, remoteEntries, remoteUnifiedLookup)
	return unifiedMap
}

func (s *server) collectLocalSessions(
	unifiedMap map[int64]UnifiedSession,
	sessions []*session.BidirectionalSession,
) {
	for _, sess := range sessions {
		unified := s.createUnifiedFromLocal(sess)
		unifiedMap[unified.UnifiedID] = unified
	}
}

func (s *server) createUnifiedFromLocal(sess *session.BidirectionalSession) UnifiedSession {
	unified := UnifiedSession{
		UnifiedID: sess.GetID(),
		ActualID:  sess.GetID(),
		OwnerID:   sess.GetParentSessionID(),
		Key:       newSessionKey(0, nil, sess.GetID()),
		BaseInfo:  sess.GetPeerInfo(),
	}
	if sess.GetRouter() != nil || (sess.GetSSHClient() != nil && !sess.GetIsListener()) {
		if addr := sess.GetRemoteAddr(); addr != nil {
			unified.BaseInfo.Hostname = addr.String()
		}
	}
	unified.Role = sess.GetPeerRole().String()
	unified.WorkingDir = sess.GetSftpWorkingDir()
	unified.IsGateway = sess.GetIsGateway()
	return unified
}

func (s *server) collectRemoteSessions(
	localSessions []*session.BidirectionalSession,
) []remoteSessionEntry {
	var entries []remoteSessionEntry
	visited := []string{fmt.Sprintf("%s:%d", s.fingerprint, s.port)}

	for _, sess := range localSessions {
		if !sess.GetIsGateway() || sess.GetSSHClient() == nil {
			continue
		}
		remoteSessions, err := sess.GetRemoteSessions(visited)
		if err != nil {
			continue
		}
		for _, remoteSession := range remoteSessions {
			entries = append(entries, remoteSessionEntry{
				rs:             remoteSession,
				gatewayID:      sess.GetID(),
				gatewayUnified: sess.GetID(),
			})
		}
	}
	return entries
}

func (s *server) buildRemoteLookup(
	entries []remoteSessionEntry,
) map[SessionKey]int64 {
	lookup := make(map[SessionKey]int64)
	for _, entry := range entries {
		key := newSessionKey(entry.gatewayID, entry.rs.Path, entry.rs.ID)
		lookup[key] = s.stableUnifiedID(key)
	}
	return lookup
}

func (s *server) processRemoteSessions(
	unifiedMap map[int64]UnifiedSession,
	entries []remoteSessionEntry,
	lookup map[SessionKey]int64,
) {
	for _, entry := range entries {
		unified := s.createUnifiedFromRemote(entry, lookup)
		unifiedMap[unified.UnifiedID] = unified
	}
}

func (s *server) createUnifiedFromRemote(
	entry remoteSessionEntry,
	lookup map[SessionKey]int64,
) UnifiedSession {
	remote := entry.rs
	key := newSessionKey(entry.gatewayID, remote.Path, remote.ID)
	return UnifiedSession{
		UnifiedID:      lookup[key],
		ActualID:       remote.ID,
		OwnerID:        s.resolveRemoteOwner(entry, lookup),
		GatewayID:      entry.gatewayID,
		Key:            key,
		BaseInfo:       remote.BaseInfo,
		Role:           remote.Role,
		WorkingDir:     remote.WorkingDir,
		IsConnector:    remote.IsConnector,
		IsGateway:      remote.IsGateway,
		ConnectionAddr: remote.ConnectionAddr,
		Path:           remote.Path,
	}
}

func (s *server) resolveRemoteOwner(
	entry remoteSessionEntry,
	lookup map[SessionKey]int64,
) int64 {
	remote := entry.rs
	if len(remote.Path) == 0 {
		return entry.gatewayUnified
	}

	if remote.ParentSessionID != 0 {
		parentKey := newSessionKey(entry.gatewayID, remote.Path, remote.ParentSessionID)
		if parentUnified, found := lookup[parentKey]; found {
			return parentUnified
		}
	}

	parentPath := remote.Path[:len(remote.Path)-1]
	parentKey := newSessionKey(
		entry.gatewayID,
		parentPath,
		remote.Path[len(remote.Path)-1],
	)
	if parentUnified, found := lookup[parentKey]; found {
		return parentUnified
	}
	return entry.gatewayUnified
}

func newSessionKey(gatewayID int64, path []int64, actualID int64) SessionKey {
	var encodedPath strings.Builder
	for i, id := range path {
		if i > 0 {
			encodedPath.WriteByte('/')
		}
		encodedPath.WriteString(strconv.FormatInt(id, 10))
	}
	return SessionKey{
		GatewayID: gatewayID,
		Path:      encodedPath.String(),
		ActualID:  actualID,
	}
}

func (s *server) stableUnifiedID(key SessionKey) int64 {
	s.unifiedSessionMutex.Lock()
	defer s.unifiedSessionMutex.Unlock()

	if id, ok := s.unifiedSessionIDs[key]; ok {
		return id
	}
	if s.unifiedSessionIDs == nil {
		s.unifiedSessionIDs = make(map[SessionKey]int64)
	}
	id := session.ReserveSessionID()
	s.unifiedSessionIDs[key] = id
	return id
}

func (unified UnifiedSession) stateKey(kind remoteStateKind) remoteStateKey {
	return remoteStateKey{Session: unified.Key, Kind: kind}
}
