package socks

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"slider/pkg/conf"
	"slider/pkg/sio"
	"slider/pkg/slog"
	"slider/pkg/types"

	"golang.org/x/crypto/ssh"
)

const (
	socksVersion             = 0x05
	socksMethodCount         = 0x01
	socksNoAuthentication    = 0x00
	socksCommandConnect      = 0x01
	socksReserved            = 0x00
	socksAddressTypeIPv4     = 0x01
	socksAddressTypeDomain   = 0x03
	socksAddressTypeIPv6     = 0x04
	socksMaxDomainLength     = 255
	socksMethodResponseBytes = 2
	socksReplyHeaderBytes    = 4
	socksIPv4AddressBytes    = 4
	socksIPv6AddressBytes    = 16
	socksPortBytes           = 2
)

// Client represents a SOCKS5 client that connects through an SSH channel
type Client struct {
	logger    *slog.Logger
	sessionID int64
	opener    ChannelOpener
}

// ChannelOpener defines the interface for opening SSH channels
type ChannelOpener interface {
	OpenChannel(name string, payload []byte) (ssh.Channel, <-chan *ssh.Request, error)
}

// NewClient creates a new SOCKS5 client
func NewClient(logger *slog.Logger, sessionID int64, opener ChannelOpener) *Client {
	return &Client{
		logger:    logger,
		sessionID: sessionID,
		opener:    opener,
	}
}

func (c *Client) Serve(conn net.Conn) error {
	return c.HandleConnection(conn)
}

func (c *Client) Close() error {
	return nil
}

// HandleConnection handles a SOCKS connection by opening an SSH channel and piping data
func (c *Client) HandleConnection(conn net.Conn) error {
	defer func() { _ = conn.Close() }()

	socksChan, reqs, oErr := c.opener.OpenChannel(conf.SSHChannelSocks5, nil)
	if oErr != nil {
		c.logger.ErrorWith("Failed to open \"socks5\" channel",
			slog.F("session_id", c.sessionID),
			slog.F("err", oErr))
		return oErr
	}
	defer func() { _ = socksChan.Close() }()
	go ssh.DiscardRequests(reqs)

	_, _ = sio.PipeWithCancel(socksChan, conn)
	return nil
}

// ConnectViaSocks establishes a connection to a destination through a SOCKS5 proxy
// This is used for direct-tcpip channel handling
func (c *Client) ConnectViaSocks(destination types.TcpIpChannelMsg, clientChannel ssh.Channel) error {
	// Open a SOCKS5 channel to the client
	socksChannel, socksRequests, oErr := c.opener.OpenChannel(conf.SSHChannelSocks5, nil)
	if oErr != nil {
		return fmt.Errorf("could not open socks5 channel - %v", oErr)
	}
	defer func() { _ = socksChannel.Close() }()

	// Discard the SSH requests from the socks channel
	go ssh.DiscardRequests(socksRequests)

	// Perform SOCKS5 handshake
	if err := c.performHandshake(socksChannel, destination); err != nil {
		return err
	}

	// Connection established, log it
	c.logger.DebugWith("connection established to destination via SOCKS",
		slog.F("session_id", c.sessionID),
		slog.F("dst_host", destination.DstHost),
		slog.F("dst_port", destination.DstPort))

	// Pipe data between the channels
	_, _ = sio.PipeWithCancel(clientChannel, socksChannel)

	return nil
}

// performHandshake performs a SOCKS5 handshake following RFC1928
func (c *Client) performHandshake(socksChannel io.ReadWriter, destination types.TcpIpChannelMsg) error {
	// Send SOCKS5 greeting: version 5, 1 method, no auth (0x00)
	if _, err := socksChannel.Write([]byte{socksVersion, socksMethodCount, socksNoAuthentication}); err != nil {
		return fmt.Errorf("could not write socks5 greeting - %v", err)
	}

	// Read server response
	respBuf := make([]byte, socksMethodResponseBytes)
	_, err := io.ReadFull(socksChannel, respBuf)
	if err != nil {
		return fmt.Errorf("could not read socks5 handshake response - %v", err)
	}

	// Verify response: version 5, method accepted (0x00)
	if respBuf[0] != socksVersion || respBuf[1] != socksNoAuthentication {
		return fmt.Errorf("invalid socks5 handshake response - %v", respBuf)
	}

	// Build connect request: version, command (CONNECT=1), reserved, address type (domain=3), host, port
	if len(destination.DstHost) > socksMaxDomainLength {
		return fmt.Errorf("destination host exceeds SOCKS5 domain length")
	}
	req := []byte{
		socksVersion,
		socksCommandConnect,
		socksReserved,
		socksAddressTypeDomain,
		byte(len(destination.DstHost)),
	}
	req = append(req, []byte(destination.DstHost)...)

	portBytes := make([]byte, socksPortBytes)
	binary.BigEndian.PutUint16(portBytes, uint16(destination.DstPort))
	req = append(req, portBytes...)

	_, err = socksChannel.Write(req)
	if err != nil {
		return fmt.Errorf("could not write socks5 handshake connect request - %v", err)
	}

	// Read connect response header
	respHdr := make([]byte, socksReplyHeaderBytes)
	_, err = io.ReadFull(socksChannel, respHdr)
	if err != nil {
		return fmt.Errorf("could not read socks5 handshake connect response - %v", err)
	}

	// Verify connection success: version 5, status success (0x00)
	if respHdr[0] != socksVersion || respHdr[1] != socksNoAuthentication {
		return fmt.Errorf("unsuccessful socks5 handshake response - %v", respHdr)
	}

	// Read the rest of the response based on address type
	var respBodyLen int
	switch respHdr[3] {
	// IPv4 address
	case socksAddressTypeIPv4:
		// 4 bytes IPv4 + 2 bytes port
		respBodyLen = socksIPv4AddressBytes + socksPortBytes
	// Domain name
	case socksAddressTypeDomain:
		domainLen := make([]byte, 1)
		if _, err = io.ReadFull(socksChannel, domainLen); err != nil {
			return fmt.Errorf("could not read socks5 domain length - %v", err)
		}
		// domain length + 2 bytes port
		respBodyLen = int(domainLen[0]) + socksPortBytes
	// IPv6 address
	case socksAddressTypeIPv6:
		// 16 bytes IPv6 + 2 bytes port
		respBodyLen = socksIPv6AddressBytes + socksPortBytes
	default:
		return fmt.Errorf("unknown address type \"%v\" socks5 handshake response", respHdr[3])
	}

	// Read the destination host + port
	respBody := make([]byte, respBodyLen)
	_, err = io.ReadFull(socksChannel, respBody)
	if err != nil {
		return fmt.Errorf("could not read destination from socks5 handshake response - %v", err)
	}

	return nil
}
