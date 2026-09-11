package server

import (
	"fmt"
	"strconv"
	"strings"

	"slider/pkg/types"
)

func parsePort(input string) (uint32, error) {
	remotePort, iErr := strconv.Atoi(input)
	if iErr != nil || remotePort < 1 || remotePort > 65535 {
		return 0, fmt.Errorf("invalid port: %s", input)
	}
	return uint32(remotePort), nil
}

func parseForwarding(input string, reverse bool, protocol string) (*types.CustomTcpIpChannelMsg, error) {
	var aAddr, bAddr string
	var aPort, bPort uint32

	portFwd := strings.Split(input, ":")

	var iErr error
	aAddr = "localhost"
	bAddr = aAddr
	switch len(portFwd) {
	case 1:
		aPort, iErr = parsePort(portFwd[0])
		if iErr != nil {
			return nil, iErr
		}
		bPort = aPort
	case 2:
		if reverse {
			aAddr = "0.0.0.0"
		}
		aPort, iErr = parsePort(portFwd[0])
		if iErr != nil {
			return nil, iErr
		}
		bPort, iErr = parsePort(portFwd[1])
		if iErr != nil {
			return nil, iErr
		}
	case 3:
		if reverse {
			aAddr = "0.0.0.0"
		}
		aPort, iErr = parsePort(portFwd[0])
		if iErr != nil {
			return nil, iErr
		}
		bAddr = portFwd[1]
		if bAddr == "" {
			bAddr = "127.0.0.1"
		}
		bPort, iErr = parsePort(portFwd[2])
		if iErr != nil {
			return nil, iErr
		}
	case 4:
		aAddr = portFwd[0]
		if aAddr == "" {
			aAddr = "127.0.0.1"
			if reverse {
				aAddr = "0.0.0.0"
			}
		}
		aPort, iErr = parsePort(portFwd[1])
		if iErr != nil {
			return nil, iErr
		}
		bAddr = portFwd[2]
		if bAddr == "" {
			bAddr = "127.0.0.1"
		}
		bPort, iErr = parsePort(portFwd[3])
		if iErr != nil {
			return nil, iErr
		}
	default:
		return nil, fmt.Errorf("invalid Port Forwarding format: %s", input)
	}

	msg := &types.CustomTcpIpChannelMsg{
		IsSshConn: false,
		Protocol:  protocol,
		TcpIpChannelMsg: &types.TcpIpChannelMsg{
			SrcHost: aAddr,
			SrcPort: aPort,
			DstHost: bAddr,
			DstPort: bPort,
		},
	}

	return msg, nil
}
