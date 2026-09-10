package server

import (
	"fmt"

	"slider/pkg/interpreter"
	"slider/pkg/session"
)

// UnifiedSession represents a normalized session (local or remote).
type UnifiedSession struct {
	UnifiedID int64
	ActualID  int64
	OwnerID   int64
	GatewayID int64
	Role      string

	BaseInfo   interpreter.BaseInfo
	WorkingDir string

	IsConnector    bool
	IsGateway      bool
	ConnectionAddr string
	Path           []int64
}

type pathKey struct {
	gatewayID int64
	pathStr   string
	actualID  int64
}

type remoteSessionEntry struct {
	rs             session.RemoteSession
	gatewayID      int64
	gatewayUnified int64
}

func (s *server) ResolveUnifiedSessions() map[int64]UnifiedSession {
	unifiedMap := make(map[int64]UnifiedSession)
	localSessions := s.GetAllSessions()
	maxID := s.collectLocalSessions(unifiedMap, localSessions)
	remoteEntries := s.collectRemoteSessions(localSessions)
	remoteUnifiedLookup := s.buildRemoteLookup(remoteEntries, &maxID)
	s.processRemoteSessions(unifiedMap, remoteEntries, remoteUnifiedLookup)
	return unifiedMap
}

func (s *server) collectLocalSessions(
	unifiedMap map[int64]UnifiedSession,
	sessions []*session.BidirectionalSession,
) int64 {
	maxID := int64(0)
	for _, sess := range sessions {
		unified := s.createUnifiedFromLocal(sess)
		unifiedMap[unified.UnifiedID] = unified
		if sess.GetID() > maxID {
			maxID = sess.GetID()
		}
	}
	return maxID
}

func (s *server) createUnifiedFromLocal(sess *session.BidirectionalSession) UnifiedSession {
	unified := UnifiedSession{
		UnifiedID: sess.GetID(),
		ActualID:  sess.GetID(),
		OwnerID:   sess.GetParentSessionID(),
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
	maxID *int64,
) map[pathKey]int64 {
	lookup := make(map[pathKey]int64)
	for _, entry := range entries {
		*maxID++
		lookup[pathKey{
			gatewayID: entry.gatewayID,
			pathStr:   fmt.Sprintf("%v", entry.rs.Path),
			actualID:  entry.rs.ID,
		}] = *maxID
	}
	return lookup
}

func (s *server) processRemoteSessions(
	unifiedMap map[int64]UnifiedSession,
	entries []remoteSessionEntry,
	lookup map[pathKey]int64,
) {
	for _, entry := range entries {
		unified := s.createUnifiedFromRemote(entry, lookup)
		unifiedMap[unified.UnifiedID] = unified
	}
}

func (s *server) createUnifiedFromRemote(
	entry remoteSessionEntry,
	lookup map[pathKey]int64,
) UnifiedSession {
	remote := entry.rs
	key := pathKey{
		gatewayID: entry.gatewayID,
		pathStr:   fmt.Sprintf("%v", remote.Path),
		actualID:  remote.ID,
	}
	return UnifiedSession{
		UnifiedID:      lookup[key],
		ActualID:       remote.ID,
		OwnerID:        s.resolveRemoteOwner(entry, lookup),
		GatewayID:      entry.gatewayID,
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
	lookup map[pathKey]int64,
) int64 {
	remote := entry.rs
	if len(remote.Path) == 0 {
		return entry.gatewayUnified
	}

	if remote.ParentSessionID != 0 {
		parentKey := pathKey{
			gatewayID: entry.gatewayID,
			pathStr:   fmt.Sprintf("%v", remote.Path),
			actualID:  remote.ParentSessionID,
		}
		if parentUnified, found := lookup[parentKey]; found {
			return parentUnified
		}
	}

	parentPath := remote.Path[:len(remote.Path)-1]
	parentKey := pathKey{
		gatewayID: entry.gatewayID,
		pathStr:   fmt.Sprintf("%v", parentPath),
		actualID:  remote.Path[len(remote.Path)-1],
	}
	if parentUnified, found := lookup[parentKey]; found {
		return parentUnified
	}
	return entry.gatewayUnified
}
