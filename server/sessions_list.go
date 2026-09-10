package server

import (
	"fmt"
	"sort"
	"text/tabwriter"
)

type sessionListState struct {
	sshPort    string
	shellPort  string
	shellTLS   string
	certID     string
	direction  string
	connection string
}

func (s *server) listSessions(ui UserInterface) {
	unifiedMap := s.ResolveUnifiedSessions()
	if len(unifiedMap) > 0 {
		s.writeSessionTable(ui, unifiedMap)
	}
	ui.PrintInfo("Active sessions: %d\n", s.activeSessionCount())
}

func (s *server) writeSessionTable(
	ui UserInterface,
	unifiedMap map[int64]UnifiedSession,
) {
	keys := make([]int, 0, len(unifiedMap))
	hasChildren := make(map[int64]bool)
	for id, unified := range unifiedMap {
		keys = append(keys, int(id))
		if unified.OwnerID != 0 {
			hasChildren[unified.OwnerID] = true
		}
	}
	sort.Ints(keys)

	writer := new(tabwriter.Writer)
	writer.Init(ui.Writer(), 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(writer, "\n\tID\tOwner\tSystem\tRole\tUser\tHost\tIO\tConnection\tSSH/SFTP\tShell/TLS\tCertID\t")
	_, _ = fmt.Fprintf(writer, "\n\t--\t-----\t------\t----\t----\t----\t--\t----------\t--------\t---------\t------\t\n")

	for _, id := range keys {
		unified := unifiedMap[int64(id)]
		state := s.sessionListState(unified)

		owner := "LOCAL"
		if unified.OwnerID != 0 {
			owner = fmt.Sprintf("%d", unified.OwnerID)
		}
		hostname := unified.BaseInfo.Hostname
		if len(hostname) > 15 {
			hostname = hostname[:15] + "..."
		}
		role := unified.Role
		if unified.IsGateway {
			role += "·G"
		} else if hasChildren[unified.UnifiedID] {
			role += "·B"
		}

		_, _ = fmt.Fprintf(writer, "\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t\n",
			unified.UnifiedID,
			owner,
			fmt.Sprintf("%s/%s", unified.BaseInfo.Arch, unified.BaseInfo.System),
			role,
			unified.BaseInfo.User,
			hostname,
			state.direction,
			state.connection,
			state.sshPort,
			fmt.Sprintf("%s/%s", state.shellPort, state.shellTLS),
			state.certID,
		)
	}
	_, _ = fmt.Fprintln(writer)
	_ = writer.Flush()
}

func (s *server) sessionListState(unified UnifiedSession) sessionListState {
	state := sessionListState{
		sshPort:    "--",
		shellPort:  "--",
		shellTLS:   "--",
		certID:     "--",
		direction:  "--",
		connection: "--",
	}
	if unified.GatewayID == 0 {
		return s.localSessionListState(unified, state)
	}
	return s.remoteSessionListState(unified, state)
}

func (s *server) localSessionListState(
	unified UnifiedSession,
	state sessionListState,
) sessionListState {
	sess, err := s.GetSession(int(unified.ActualID))
	if err != nil {
		return state
	}

	if sess.GetSSHInstance().IsEnabled() {
		if port, err := sess.GetSSHInstance().GetEndpointPort(); err == nil {
			state.sshPort = fmt.Sprintf("%d", port)
		}
	}
	if sess.GetShellInstance().IsEnabled() {
		if port, err := sess.GetShellInstance().GetEndpointPort(); err == nil {
			state.shellPort = fmt.Sprintf("%d", port)
		}
		state.shellTLS = "off"
		if sess.GetShellInstance().IsTLSOn() {
			state.shellTLS = "on"
		}
	}
	certID, _ := sess.GetCertInfo()
	if s.authOn && certID != 0 {
		state.certID = fmt.Sprintf("%d", certID)
	}
	state.direction = "<-"
	if sess.GetRole().IsConnector() {
		state.direction = "->"
	}
	if addr := sess.GetRemoteAddr(); addr != nil {
		state.connection = addr.String()
	}
	return state
}

func (s *server) remoteSessionListState(
	unified UnifiedSession,
	state sessionListState,
) sessionListState {
	state.direction = "<-"
	if unified.IsConnector {
		state.direction = "->"
	}
	state.connection = unified.ConnectionAddr

	sshKey := unified.stateKey(remoteStateSSH)
	s.remoteSessionsMutex.Lock()
	if remoteState, ok := s.remoteSessions[sshKey]; ok {
		if remoteState.SSHInstance != nil && remoteState.SSHInstance.IsEnabled() {
			if port, err := remoteState.SSHInstance.GetEndpointPort(); err == nil {
				state.sshPort = fmt.Sprintf("%d", port)
			}
		}
	}
	s.remoteSessionsMutex.Unlock()

	shellKey := unified.stateKey(remoteStateShell)
	s.remoteSessionsMutex.Lock()
	if remoteState, ok := s.remoteSessions[shellKey]; ok {
		if remoteState.ShellInstance != nil && remoteState.ShellInstance.IsEnabled() {
			if port, err := remoteState.ShellInstance.GetEndpointPort(); err == nil {
				state.shellPort = fmt.Sprintf("%d", port)
				if remoteState.ShellInstance.IsTLSOn() {
					state.shellTLS = "on"
				}
			}
		}
	}
	s.remoteSessionsMutex.Unlock()
	return state
}
