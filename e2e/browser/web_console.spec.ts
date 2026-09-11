import { expect, Page, test } from '@playwright/test';
import { execFile, spawn, type ChildProcessWithoutNullStreams } from 'node:child_process';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { promisify } from 'node:util';
import { fileURLToPath } from 'node:url';

const execFileAsync = promisify(execFile);
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');

interface RunningServer {
  baseURL: string;
  directory: string;
  output: () => string;
  process: ChildProcessWithoutNullStreams;
}

interface KeyPair {
  FingerPrint: string;
  PrivateKey: string;
}

let binaryPath = '';
let binaryDirectory = '';

test.beforeAll(async () => {
  binaryDirectory = await mkdtemp(path.join(tmpdir(), 'slider-browser-bin-'));
  binaryPath = path.join(binaryDirectory, 'slider');
  await execFileAsync('go', ['build', '-trimpath', '-o', binaryPath, '.'], {
    cwd: root,
  });
});

test.afterAll(async () => {
  await rm(binaryDirectory, { force: true, recursive: true });
});

test('controls the console on desktop and mobile viewports', async ({ page }) => {
  const server = await startServer();
  const diagnostics = observePage(page, server.baseURL);
  const terminalOutput = observeTerminalOutput(page);
  try {
    await page.goto(`${server.baseURL}/console`);

    await expect(page.locator('#status')).toHaveText('Connected to Slider console');
    await expect(page.locator('.xterm-helper-textarea')).toBeAttached();
    await sendTerminalCommand(page, 'help');

    for (const command of [
      'bg',
      'clear',
      'connect',
      'exit',
      'help',
      'portfwd',
      'sessions',
      'shell',
      'socks',
      'ssh',
    ]) {
      await expect.poll(terminalOutput).toContain(command);
    }

    for (const [command, expected] of [
      ['connect --help', '--fingerprint'],
      ['portfwd --help', '--reverse'],
      ['sessions --help', '--disconnect'],
      ['shell --help', '--interactive'],
      ['socks --help', '--local'],
      ['ssh --help', '--alt-shell'],
      ['sessions', 'Active sessions: 0'],
      ['definitely-not-a-command', 'unknown command'],
    ]) {
      await sendTerminalCommand(page, command);
      await expect.poll(terminalOutput).toContain(expected);
    }

    const initialTheme = await page.locator('body').getAttribute('data-theme');
    await page.locator('#themeToggle').click();
    const changedTheme = await page.locator('body').getAttribute('data-theme');
    expect(changedTheme).not.toBe(initialTheme);
    await page.reload();
    await expect(page.locator('body')).toHaveAttribute('data-theme', changedTheme ?? '');

    const layout = await page.evaluate(() => ({
      documentWidth: document.documentElement.scrollWidth,
      viewportWidth: window.innerWidth,
      viewportHeight: window.innerHeight,
      terminalWidth: document.querySelector('#terminal')?.getBoundingClientRect().width ?? 0,
      terminalHeight: document.querySelector('#terminal')?.getBoundingClientRect().height ?? 0,
      terminalBottom: document.querySelector('#terminal')?.getBoundingClientRect().bottom ?? 0,
      toolbarBottom: document.querySelector('.console-toolbar')?.getBoundingClientRect().bottom ?? 0,
      terminalTop: document.querySelector('#terminal')?.getBoundingClientRect().top ?? 0,
    }));
    expect(layout.documentWidth).toBeLessThanOrEqual(layout.viewportWidth);
    expect(layout.terminalWidth).toBeGreaterThan(0);
    expect(layout.terminalHeight).toBeGreaterThan(layout.viewportHeight * 0.6);
    expect(layout.terminalBottom).toBeLessThanOrEqual(layout.viewportHeight);
    expect(layout.toolbarBottom).toBeLessThanOrEqual(layout.terminalTop);
    expect(diagnostics.externalRequests).toEqual([]);
    expect(diagnostics.pageErrors).toEqual([]);
    expect(diagnostics.consoleErrors).toEqual([]);

    await expect(page.locator('#status')).toHaveText('Connected to Slider console');
    await sendTerminalCommand(page, 'bg');
    await expect(page.locator('#status')).toHaveText('Disconnected from server');
  } finally {
    await stopServer(server);
  }
});

test('authenticates, exposes cert commands, and logs out', async ({ page }) => {
  const directory = await mkdtemp(path.join(tmpdir(), 'slider-browser-auth-'));
  const certPath = path.join(directory, 'listener.crt');
  const keyPath = path.join(directory, 'listener.key');
  const certJarPath = path.join(directory, 'certs.json');
  await execFileAsync(
    'openssl',
    [
      'req',
      '-x509',
      '-newkey',
      'rsa:2048',
      '-nodes',
      '-keyout',
      keyPath,
      '-out',
      certPath,
      '-days',
      '1',
      '-subj',
      '/CN=localhost',
      '-addext',
      'subjectAltName=DNS:localhost,IP:127.0.0.1',
    ],
    { cwd: directory },
  );

  const server = await startServer([
    '--auth',
    '--ca-store',
    '--certs',
    certJarPath,
    '--listener-cert',
    certPath,
    '--listener-key',
    keyPath,
  ], directory, 'https');
  const diagnostics = observePage(page, server.baseURL);
  const terminalOutput = observeTerminalOutput(page);
  try {
    await page.goto(`${server.baseURL}/console`);
    await expect(page).toHaveURL(/\/auth$/);

    await page.locator('#fingerprint').fill('invalid');
    await page.locator('#privateKey').fill('invalid');
    await page.locator('#submitBtn').click();
    await expect(page.locator('#error')).toContainText('Certificate fingerprint not found');
    diagnostics.consoleErrors = diagnostics.consoleErrors.filter((message) => !message.includes('401'));

    const certJar = JSON.parse(await readFile(certJarPath, 'utf8')) as Record<string, KeyPair>;
    await page.locator('#fingerprint').fill(certJar['1'].FingerPrint);
    await page.locator('#privateKey').fill(certJar['1'].PrivateKey);
    await page.locator('#submitBtn').click();

    await expect(page).toHaveURL(/\/console$/);
    await expect(page.locator('#status')).toHaveText('Connected to Slider console');
    await sendTerminalCommand(page, 'help');
    await expect.poll(terminalOutput).toContain('certs');

    await page.locator('#logoutBtn').click();
    await expect(page).toHaveURL(/\/auth$/);
    expect(diagnostics.externalRequests).toEqual([]);
    expect(diagnostics.pageErrors).toEqual([]);
    expect(diagnostics.consoleErrors).toEqual([]);
  } finally {
    await stopServer(server);
  }
});

interface PageDiagnostics {
  consoleErrors: string[];
  externalRequests: string[];
  pageErrors: string[];
}

function observePage(page: Page, baseURL: string): PageDiagnostics {
  const diagnostics: PageDiagnostics = {
    consoleErrors: [],
    externalRequests: [],
    pageErrors: [],
  };
  const expectedOrigin = new URL(baseURL).origin;

  page.on('request', (request) => {
    const url = new URL(request.url());
    if ((url.protocol === 'http:' || url.protocol === 'https:') && url.origin !== expectedOrigin) {
      diagnostics.externalRequests.push(request.url());
    }
  });
  page.on('pageerror', (error) => {
    diagnostics.pageErrors.push(error.message);
  });
  page.on('console', (message) => {
    if (message.type() === 'error') {
      diagnostics.consoleErrors.push(message.text());
    }
  });

  return diagnostics;
}

function observeTerminalOutput(page: Page): () => string {
  let output = '';
  page.on('websocket', (socket) => {
    socket.on('framereceived', (event) => {
      output += typeof event.payload === 'string'
        ? event.payload
        : event.payload.toString('utf8');
    });
  });
  return () => output;
}

async function sendTerminalCommand(page: Page, command: string): Promise<void> {
  const input = page.locator('.xterm-helper-textarea');
  await input.focus();
  await page.keyboard.type(command);
  await page.keyboard.press('Enter');
}

async function startServer(
  additionalArgs: string[] = [],
  directory?: string,
  scheme = 'http',
): Promise<RunningServer> {
  const serverDirectory = directory ?? await mkdtemp(path.join(tmpdir(), 'slider-browser-'));
  const port = await reservePort();
  const args = [
    'server',
    '--headless',
    '--http-console',
    '--address',
    '127.0.0.1',
    '--port',
    String(port),
    '--keepalive',
    '5s',
    '--colorless',
    '--verbose',
    'debug',
    ...additionalArgs,
  ];
  const child = spawn(binaryPath, args, {
    cwd: serverDirectory,
    env: {
      ...process.env,
      NO_COLOR: '1',
      S_HOME: `${path.join(serverDirectory, '.slider')}${path.sep}`,
      TERM: 'xterm-256color',
    },
    stdio: 'pipe',
  });

  let processOutput = '';
  child.stdout.on('data', (chunk) => {
    processOutput += chunk.toString();
  });
  child.stderr.on('data', (chunk) => {
    processOutput += chunk.toString();
  });
  await waitForOutput(child, () => processOutput, 'Starting listener');

  return {
    baseURL: `${scheme}://localhost:${port}`,
    directory: serverDirectory,
    output: () => processOutput,
    process: child,
  };
}

async function stopServer(server: RunningServer): Promise<void> {
  if (server.process.exitCode === null) {
    server.process.kill('SIGTERM');
    await Promise.race([
      new Promise<void>((resolve) => server.process.once('exit', () => resolve())),
      new Promise<void>((resolve) => setTimeout(resolve, 2_000)),
    ]);
  }
  if (server.process.exitCode === null) {
    server.process.kill('SIGKILL');
  }
  await rm(server.directory, { force: true, recursive: true });
}

async function waitForOutput(
  process: ChildProcessWithoutNullStreams,
  output: () => string,
  expected: string,
): Promise<void> {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    if (output().includes(expected)) {
      return;
    }
    if (process.exitCode !== null) {
      throw new Error(`server exited with ${process.exitCode}\n${output()}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  throw new Error(`timeout waiting for ${expected}\n${output()}`);
}

async function reservePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const address = server.address();
      if (typeof address === 'string' || address === null) {
        server.close();
        reject(new Error('failed to reserve TCP port'));
        return;
      }
      server.close((error) => {
        if (error) {
          reject(error);
          return;
        }
        resolve(address.port);
      });
    });
  });
}
