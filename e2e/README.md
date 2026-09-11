# Slider E2E tests

This directory contains black-box tests for the shipped `slider` binary. The
tests build the binary once, allocate ephemeral ports and isolated working
directories, and run real Server, Client, Gateway and Beacon processes.

## Test layers

| Layer | Command | Purpose | Expected duration |
| --- | --- | --- | --- |
| Unit/integration | `make test` | Package behavior and error branches | under 1 minute |
| Native E2E | `make test-e2e` | Processes, PTYs, WebSockets, SSH/SFTP and tunnels | under 1 minute |
| Browser E2E | `make test-e2e-web` | Desktop/mobile Web Console and authentication UI | under 1 minute |
| Extended E2E | `make test-e2e-extended` | Interactive Vim and 1 GiB upload/download | under 15 minutes |

The browser suite builds the frontend and exercises the assets embedded in the
Slider binary. It fails if the application requests resources from an external
origin.

## Functional coverage

Functional coverage means every supported user-facing feature area below has
at least one automated black-box path. It does not mean 100% Go statement or
branch coverage.

| Feature area | Normal path | Boundary/error path | Test |
| --- | --- | --- | --- |
| Root, Server, Client and Hook CLI | All commands and primary flags are discoverable | Invalid argument and conflicting flag rejection | `TestCLIValidation` |
| Local Console | Every registered command is reachable through a real PTY | Unknown commands and conflicting flags | `TestLocalConsoleCommandSurface` |
| WebSocket Console | Every registered command is reachable over `/console/ws` | Non-upgrade request, unknown commands, reconnect after `bg` | `TestWebConsoleCommandSurface`, `TestHTTPControlEndpoints` |
| Browser Console | xterm input/output, theme persistence and logout | Desktop and mobile viewport overflow checks | `web_console.spec.ts` |
| Web authentication | Challenge signing, JWT cookie and authenticated WebSocket | Unknown fingerprint and unauthenticated redirect | `TestAuthenticatedWebConsole`, `web_console.spec.ts` |
| Certificate management | List/create/remove from local and Web consoles; SSH key and CA dump | Unauthorized certificate rejection | `TestLocalConsoleCertificateCommands`, `TestAuthenticatedWebConsole` |
| Session lifecycle | List, interact, disconnect and kill | Unknown IDs and mutually exclusive actions | `TestWebConsoleAgentWorkflow`, `TestSessionDisconnect` |
| Remote execution | Execute a command through direct and routed sessions | Missing command handling | `TestWebConsoleAgentWorkflow`, topology tests |
| SFTP navigation/listing | Local/remote pwd, cd and ls plus aliases | Missing paths and invalid argument counts | `TestWebConsoleAgentWorkflow` |
| SFTP mutation | mkdir, stat, chmod, rename/move and remove | Directory recursion and paths containing spaces | `TestWebConsoleAgentWorkflow` |
| SFTP transfer | File and recursive directory upload/download | Missing source and 1 GiB transfer | `TestWebConsoleAgentWorkflow`, `TestLargeFileRoundTrip` |
| Interactive Shell | Endpoint startup, command I/O and shutdown | Full-screen Vim over PTY | `TestWebConsoleAgentWorkflow`, `TestInteractiveShellVim` |
| SSH endpoint | External SSH client command execution | Endpoint lifecycle | `TestWebConsoleAgentWorkflow` |
| SOCKS5 | Local and session proxy data round trips | Endpoint lifecycle and invalid flags | `TestWebConsoleAgentWorkflow`, command-surface tests |
| Port forwarding | Local/reverse TCP and UDP data round trips | Removal and invalid flag combinations | `TestWebConsoleAgentWorkflow`, command-surface tests |
| Reverse Client | Authenticated SSH-over-WebSocket session | Disconnect/retry and process shutdown | `TestAgentConnectsAndReportsPlatform`, `TestReverseClientReconnects` |
| Listener Client | TLS and mTLS connection initiated by Server | CLI mode constraints | `TestListenerClientTopology`, `TestCLIValidation` |
| Beacon | Child Agent tunnel through Beacon | Multi-session discovery | `TestBeaconTopology` |
| Gateway | Authenticated operator connection and routed execution | Role/fingerprint requirements | `TestGatewayTopology` |
| Callback | Authenticated Gateway callback and routed execution | Startup prerequisite validation | `TestGatewayCallbackTopology`, package tests |
| HTTP presentation | Root redirect, template, status, header, health and version | Missing route and wrong method | `TestHTTPControlEndpoints`, `TestServerAndClientHTTPPresentation`, `TestHTTPRedirect` |
| Platform adaptation | Native Agent handshake on Linux, macOS and Windows | Unix PTY/Vim and Windows-specific package tests | CI `native-e2e` matrix |

Android and iOS Agent binaries do not exist in this repository. Mobile
coverage therefore applies to the supported browser control surface, while
native controlled-side coverage follows the release targets: Linux, macOS and
Windows on the architectures built by the release pipeline.

## Stability rules

- Ports are selected by the OS and each test receives isolated directories.
- Readiness is based on protocol output, never fixed startup sleeps.
- Every network operation has a deadline.
- Test processes are interrupted and then force-killed during cleanup.
- Browser requests are restricted to the Slider server origin.
- Tests do not retry internally. CI retries the browser suite once and keeps a
  Playwright trace for the retry.
- Large and interactive scenarios are separate from the PR-critical path.

## Reports

`npm run test:e2e:web` writes the visual Playwright report to
`playwright-report/`. CI publishes that directory and JUnit reports from the Go
suite as build artifacts and check annotations.
