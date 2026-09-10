package server

import (
	"errors"
	"fmt"
	"slider/pkg/conf"
	"slider/pkg/listener"
	"time"

	"github.com/spf13/pflag"
)

const (
	// Console Connect Command
	connectCmd   = "connect"
	connectDesc  = "Establishes a connection to a Client"
	connectUsage = "Usage: connect [flags] <host_address:port>"
)

// ConnectCommand implements the 'connect' command
type ConnectCommand struct{ BaseCommand }

func (c *ConnectCommand) Name() string        { return connectCmd }
func (c *ConnectCommand) Description() string { return connectDesc }
func (c *ConnectCommand) Usage() string       { return connectUsage }
func (c *ConnectCommand) Run(ctx *ExecutionContext, args []string) error {
	svr := ctx.getServer()
	ui := ctx.UI()

	connectFlags := pflag.NewFlagSet(connectCmd, pflag.ContinueOnError)
	connectFlags.SetOutput(ui.Writer())

	cCert := connectFlags.Int64P("cert-id", "i", 0, "Specify certID for SSH key authentication")
	cDNS := connectFlags.StringP("dns", "d", "", "Use custom DNS resolver")
	cProto := connectFlags.StringP("proto", "p", conf.Proto, "Use custom proto")
	cTlsCert := connectFlags.StringP("tls-cert", "t", "", "Use custom client TLS certificate")
	cTlsKey := connectFlags.StringP("tls-key", "k", "", "Use custom client TLS key")
	cFingerprint := connectFlags.StringP("fingerprint", "f", "", "Expected SSH host fingerprint")
	cCA := connectFlags.String("ca", "", "CA certificate for server verification")
	cServerName := connectFlags.String("server-name", "", "Server name for TLS verification")
	cGateway := connectFlags.BoolP("gateway", "g", false, "Connect to another server in gateway mode")
	var cCallback *bool
	if svr.gateway {
		cCallback = connectFlags.BoolP("callback", "b", false, "Connect to server and offer control")
	}

	connectFlags.Usage = func() {
		_, _ = fmt.Fprintf(ui.Writer(), "%s\n", connectDesc)
		_, _ = fmt.Fprintf(ui.Writer(), "Usage: %s\n\n", connectUsage)
		connectFlags.PrintDefaults()
		_, _ = fmt.Fprintf(ui.Writer(), "\n")
	}

	if pErr := connectFlags.Parse(args); pErr != nil {
		if errors.Is(pErr, pflag.ErrHelp) {
			return nil
		}
		return pErr
	}
	if (*cTlsCert == "") != (*cTlsKey == "") {
		return fmt.Errorf("--tls-cert and --tls-key must be provided together")
	}

	// Validate exact args
	if connectFlags.NArg() != 1 {
		return fmt.Errorf("exactly 1 argument(s) required, got %d", connectFlags.NArg())
	}

	clientURL := connectFlags.Args()[0]
	cu, uErr := listener.ResolveURL(clientURL)
	if uErr != nil {
		return fmt.Errorf("failed to resolve URL: %w", uErr)
	}

	// Determine operation type
	operation := conf.OperationGateway
	if *cGateway {
		operation = conf.OperationOperator
	} else if cCallback != nil && *cCallback {
		operation = conf.OperationCallback
	}
	if operation == conf.OperationOperator && *cFingerprint == "" {
		return fmt.Errorf("--gateway requires --fingerprint")
	}
	if operation == conf.OperationOperator && (*cCert == 0 || !svr.authOn) {
		return fmt.Errorf("--gateway requires server authentication and --cert-id")
	}
	if operation == conf.OperationCallback && *cCert == 0 {
		return fmt.Errorf("--callback requires --cert-id")
	}
	if operation == conf.OperationCallback && !svr.authOn {
		return fmt.Errorf("--callback requires server authentication")
	}
	if operation != conf.OperationOperator && cu.Scheme != "https" {
		return fmt.Errorf("listener and callback connections require HTTPS")
	}

	ui.PrintInfo("Establishing Connection to %s (Timeout: %s)", cu.String(), conf.Timeout)

	notifier := make(chan error, 1)
	ticker := time.NewTicker(conf.ConnectTickerInterval)
	defer ticker.Stop()
	timeout := time.After(conf.Timeout)

	go svr.newConnector(
		cu,
		notifier,
		*cCert,
		*cDNS,
		*cProto,
		connectorSecurity{
			fingerprint: *cFingerprint,
			caPath:      *cCA,
			serverName:  *cServerName,
			tlsCertPath: *cTlsCert,
			tlsKeyPath:  *cTlsKey,
		},
		operation,
	)

	for {
		// Priority check: always check notifier first
		select {
		case err := <-notifier:
			if err != nil {
				return fmt.Errorf("connection failed: %w", err)
			}
			ui.PrintSuccess("Connection established")
			return nil
		default:
			// Non-blocking, fall through to ticker/timeout
		}

		// Then check ticker and timeout
		select {
		case <-ticker.C:
			ui.FlatPrintf(".")
		case <-timeout:
			return fmt.Errorf("connection timeout")
		}
	}
}
