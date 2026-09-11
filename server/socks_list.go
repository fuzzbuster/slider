package server

import (
	"fmt"
	"text/tabwriter"
)

// listSocksSessions displays all active SOCKS sessions in a table
func listSocksSessions(svr *server, ui UserInterface) error {
	totalSocks := 0

	sessionList := svr.GetAllSessions()
	for _, sess := range sessionList {
		if sess.GetSocksInstance() != nil && sess.GetSocksInstance().IsEnabled() {
			totalSocks++
		}
	}
	svr.localSocks.mu.Lock()
	if svr.localSocks.server != nil {
		totalSocks++
	}
	svr.localSocks.mu.Unlock()

	if totalSocks > 0 {
		tw := new(tabwriter.Writer)
		tw.Init(ui.Writer(), 0, 4, 2, ' ', 0)

		_, _ = fmt.Fprintf(tw, "\n\tType\tID\tPort\t")
		_, _ = fmt.Fprintf(tw, "\n\t----\t--\t----\t\n")

		svr.localSocks.mu.Lock()
		if svr.localSocks.server != nil {
			_, _ = fmt.Fprintf(tw, "\tLOCAL\t--\t%d\t\n", svr.localSocks.server.Port())
		}
		svr.localSocks.mu.Unlock()

		for _, sess := range sessionList {
			if sess.GetSocksInstance() != nil && sess.GetSocksInstance().IsEnabled() {
				port, pErr := sess.GetSocksInstance().GetEndpointPort()
				if pErr != nil {
					port = 0
				}
				_, _ = fmt.Fprintf(tw, "\tSESSION\t%d\t%d\t\n", sess.GetID(), port)
			}
		}

		unifiedMap := svr.ResolveUnifiedSessions()
		for unifiedID, unified := range unifiedMap {
			if unified.GatewayID != 0 {
				socksKey := unified.stateKey(remoteStateSocks)
				svr.remoteSessionsMutex.Lock()
				if state, ok := svr.remoteSessions[socksKey]; ok {
					if state.SocksInstance != nil && state.SocksInstance.IsEnabled() {
						port, pErr := state.SocksInstance.GetEndpointPort()
						if pErr != nil {
							port = 0
						}
						_, _ = fmt.Fprintf(tw, "\tSESSION\t%d\t%d\t\n", unifiedID, port)
						totalSocks++
					}
				}
				svr.remoteSessionsMutex.Unlock()
			}
		}

		_, _ = fmt.Fprintln(tw)
		_ = tw.Flush()
	}
	ui.PrintInfo("Active SOCKS servers: %d\n", totalSocks)
	return nil
}
