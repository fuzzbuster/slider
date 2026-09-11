import { FitAddon } from '@xterm/addon-fit';
import { Terminal } from '@xterm/xterm';
import '@xterm/xterm/css/xterm.css';

import './styles/base.css';
import './styles/console.css';

import { initializeTheme } from './theme';

type ConnectionState = 'connected' | 'connecting' | 'disconnected';

function requiredElement<T extends HTMLElement>(
  selector: string,
  constructor: { new (): T },
): T {
  const element = document.querySelector(selector);
  if (!(element instanceof constructor)) {
    throw new Error(`Required element is missing: ${selector}`);
  }
  return element;
}

function webSocketURL(path: string): string {
  const url = new URL(path, window.location.href);
  url.protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
  return url.toString();
}

initializeTheme();

const statusElement = requiredElement('#status', HTMLDivElement);
const statusText = requiredElement('[data-status-text]', HTMLSpanElement);
const terminalElement = requiredElement('#terminal', HTMLDivElement);
const logoutButton = requiredElement('#logoutBtn', HTMLButtonElement);
const authEnabled = document.body.dataset.authOn === 'true';
const authPath = document.body.dataset.authPath || '/auth';
const authLogoutPath = document.body.dataset.authLogoutPath || `${authPath}/logout`;
const consoleWebSocketPath = document.body.dataset.consoleWsPath || '/console/ws';

logoutButton.hidden = !authEnabled;
logoutButton.addEventListener('click', async () => {
  try {
    await fetch(authLogoutPath, {
      method: 'POST',
      credentials: 'same-origin',
    });
  } catch (error) {
    console.error('Logout error:', error);
  }
  window.location.assign(authPath);
});

let terminal: Terminal | null = null;
let fitAddon: FitAddon | null = null;
let socket: WebSocket | null = null;

function setStatus(state: ConnectionState, message: string): void {
  statusElement.className = `status ${state}`;
  statusText.textContent = message;
}

function sendTerminalSize(): void {
  if (!terminal || !fitAddon) {
    return;
  }

  fitAddon.fit();
  if (socket?.readyState === WebSocket.OPEN) {
    socket.send(JSON.stringify({
      type: 'resize',
      cols: terminal.cols,
      rows: terminal.rows,
    }));
  }
}

function initializeTerminal(): void {
  terminal = new Terminal({
    cursorBlink: true,
    fontSize: 13,
    fontFamily: 'Menlo, Monaco, "Courier New", monospace',
    scrollback: 10000,
    convertEol: true,
    theme: {
      background: '#101214',
      foreground: '#e7ebee',
      cursor: '#e7ebee',
      black: '#000000',
      red: '#cd3131',
      green: '#0dbc79',
      yellow: '#e5e510',
      blue: '#2472c8',
      magenta: '#bc3fbc',
      cyan: '#11a8cd',
      white: '#e5e5e5',
      brightBlack: '#666666',
      brightRed: '#f14c4c',
      brightGreen: '#23d18b',
      brightYellow: '#f5f543',
      brightBlue: '#3b8eea',
      brightMagenta: '#d670d6',
      brightCyan: '#29b8db',
      brightWhite: '#ffffff',
    },
  });

  fitAddon = new FitAddon();
  terminal.loadAddon(fitAddon);
  terminal.open(terminalElement);
  terminal.focus();

  terminal.onKey(({ domEvent }) => {
    if (domEvent.key === 'Tab') {
      domEvent.preventDefault();
    }
  });
  terminal.onData((data) => {
    if (socket?.readyState === WebSocket.OPEN) {
      socket.send(data);
    }
  });

  sendTerminalSize();
  window.setTimeout(sendTerminalSize, 100);
}

function writeTerminalData(data: string | Blob | ArrayBuffer): void {
  if (!terminal) {
    return;
  }
  if (data instanceof Blob) {
    void data.arrayBuffer().then((buffer) => {
      terminal?.write(new Uint8Array(buffer));
      terminal?.scrollToBottom();
    });
    return;
  }
  if (data instanceof ArrayBuffer) {
    terminal.write(new Uint8Array(data));
  } else {
    terminal.write(data);
  }
  terminal.scrollToBottom();
}

function connectWebSocket(): void {
  setStatus('connecting', 'Connecting...');
  socket = new WebSocket(webSocketURL(consoleWebSocketPath));

  socket.addEventListener('open', () => {
    setStatus('connected', 'Connected to Slider console');
    initializeTerminal();
  });
  socket.addEventListener('message', (event: MessageEvent<string | Blob | ArrayBuffer>) => {
    writeTerminalData(event.data);
  });
  socket.addEventListener('error', (error) => {
    console.error('WebSocket error:', error);
    setStatus('disconnected', 'Connection error');
    terminal?.writeln('\r\n\x1b[31mWebSocket error occurred\x1b[0m');
  });
  socket.addEventListener('close', () => {
    setStatus('disconnected', 'Disconnected from server');
    terminal?.writeln('\r\n\x1b[33mConnection closed\x1b[0m');
  });
}

window.addEventListener('resize', sendTerminalSize);
connectWebSocket();
