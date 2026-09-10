package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"

	"slider/pkg/listener"
	"slider/pkg/session"
	"slider/pkg/slog"
	"slider/pkg/types"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
	"golang.org/x/term"
)

func (s *server) handleWebSocketConsole(w http.ResponseWriter, r *http.Request) error {
	if s.authOn {
		token := extractTokenFromRequest(r)
		if token == "" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("Missing authentication token"))
			return fmt.Errorf("missing token")
		}

		fingerprint, certID, err := s.validateToken(token)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("Invalid or expired token"))
			return err
		}

		s.DebugWith("WebSocket console authenticated",
			slog.F("remote_addr", r.RemoteAddr),
			slog.F("fingerprint", fingerprint),
			slog.F("cert_id", certID))
	}

	wsConn, err := listener.NewWebSocketUpgrader().Upgrade(w, r, nil)
	if err != nil {
		return err
	}
	defer func() { _ = wsConn.Close() }()

	ptyMaster, ptyTTY, err := pty.Open()
	if err != nil {
		return err
	}
	defer func() { _ = ptyMaster.Close() }()
	defer func() { _ = ptyTTY.Close() }()

	if err := pty.Setsize(ptyMaster, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
		_ = wsConn.WriteMessage(websocket.TextMessage, []byte("Failed to set PTY size"))
		return err
	}

	history := session.DefaultHistory
	webConsole, err := s.newWebConsole(ptyTTY, history)
	if err != nil {
		return err
	}

	done := make(chan struct{})
	defer close(done)
	var currentConsole atomic.Pointer[Console]
	currentConsole.Store(webConsole)
	go bridgeWebSocketToPTY(wsConn, ptyMaster, &currentConsole, done)
	go bridgePTYToWebSocket(ptyMaster, wsConn, done)

	s.consoleBanner(webConsole)
	for {
		line, err := webConsole.Term.ReadLine()
		if err != nil {
			if err != io.EOF {
				webConsole.PrintError("Failed to read input: %s", err)
			}
			replacement, replacementErr := s.newWebConsole(ptyTTY, history)
			if replacementErr != nil {
				return replacementErr
			}
			webConsole = replacement
			currentConsole.Store(webConsole)
			_, _ = webConsole.Term.Write([]byte("\r\n"))
			continue
		}

		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}

		ctx := &ExecutionContext{server: s, ui: webConsole}
		command := strings.ToLower(parts[0])
		if err := s.commandRegistry.Execute(ctx, command, parts[1:]); err != nil {
			if errors.Is(err, ErrExitConsole) {
				webConsole.PrintlnGreyOut("Disconnecting...")
				return nil
			}
			s.ErrorWith("Failed to execute command",
				slog.F("command", command),
				slog.F("args", parts[1:]),
				slog.F("err", err))
			webConsole.PrintError("Error: %v", err)
		}
		webConsole.Term.SetPrompt(getPrompt())
	}
}

func bridgeWebSocketToPTY(
	conn *websocket.Conn,
	ptyMaster *os.File,
	currentConsole *atomic.Pointer[Console],
	done <-chan struct{},
) {
	defer func() { _ = ptyMaster.Close() }()
	for {
		select {
		case <-done:
			return
		default:
			messageType, message, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if messageType == websocket.TextMessage &&
				handleCurrentConsoleResize(message, ptyMaster, currentConsole) {
				continue
			}
			if _, err := ptyMaster.Write(message); err != nil {
				return
			}
		}
	}
}

func handleCurrentConsoleResize(
	message []byte,
	ptyMaster *os.File,
	currentConsole *atomic.Pointer[Console],
) bool {
	return handleResizeMessage(message, ptyMaster, currentConsole.Load())
}

func handleResizeMessage(message []byte, ptyMaster *os.File, console *Console) bool {
	if !strings.HasPrefix(string(message), "{\"type\":\"resize\"") {
		return false
	}

	var resize struct {
		Type string `json:"type"`
		Cols uint16 `json:"cols"`
		Rows uint16 `json:"rows"`
	}
	if err := json.Unmarshal(message, &resize); err != nil {
		return true
	}
	if console == nil {
		return true
	}

	_ = pty.Setsize(ptyMaster, &pty.Winsize{Rows: resize.Rows, Cols: resize.Cols})
	_ = console.Term.SetSize(int(resize.Cols), int(resize.Rows))
	if console.ResizeChan != nil {
		select {
		case console.ResizeChan <- types.TermDimensions{
			Width:  uint32(resize.Cols),
			Height: uint32(resize.Rows),
		}:
		default:
		}
	}
	return true
}

func bridgePTYToWebSocket(
	ptyMaster *os.File,
	conn *websocket.Conn,
	done <-chan struct{},
) {
	buffer := make([]byte, 4096)
	for {
		select {
		case <-done:
			return
		default:
			n, err := ptyMaster.Read(buffer)
			if err != nil {
				return
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, buffer[:n]); err != nil {
				return
			}
		}
	}
}

func (s *server) newWebConsole(ptyTTY *os.File, history *session.CustomHistory) (*Console, error) {
	if _, err := term.MakeRaw(int(ptyTTY.Fd())); err != nil {
		return nil, err
	}
	webConsole := &Console{
		Term:       term.NewTerminal(ptyTTY, getPrompt()),
		ReadWriter: ptyTTY,
		History:    history,
		ResizeChan: make(chan types.TermDimensions, 10),
	}
	if s.commandRegistry == nil {
		s.initRegistry()
	}
	webConsole.setConsoleAutoComplete(s.commandRegistry, s.serverInterpreter)
	return webConsole, nil
}
