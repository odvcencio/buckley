const { app, BrowserWindow } = require('electron');
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
const { spawn } = require('node:child_process');

let backendProcess;

const IPC_BIND = process.env.BUCKLEY_IPC_BIND || '127.0.0.1:4488';
const AUTH_TOKEN = process.env.BUCKLEY_IPC_TOKEN || '';
const BUCKLEY_BIN = resolveBuckleyBin(process.env.BUCKLEY_BIN);
const UI_ASSETS_DIR = process.env.BUCKLEY_UI_ASSETS_DIR || '';
const bindInfo = parseBind(IPC_BIND);
const DEFAULT_UI_URL = `http://${formatHost(bindInfo.host)}:${bindInfo.port}`;
const UI_URL = normalizeURL(process.env.BUCKLEY_UI_URL || process.env.VITE_DEV_SERVER_URL || DEFAULT_UI_URL);
const SPAWN_BACKEND = process.env.BUCKLEY_SPAWN_BACKEND === '1' || !process.env.BUCKLEY_UI_URL;

const isDev = Boolean(process.env.VITE_DEV_SERVER_URL);

function startBackend() {
  if (!SPAWN_BACKEND) {
    return;
  }
  if (backendProcess) {
    return;
  }
  const args = ['serve', '--bind', IPC_BIND, '--browser'];
  if (AUTH_TOKEN) {
    args.push('--auth-token', AUTH_TOKEN);
  }
  if (UI_ASSETS_DIR) {
    args.push('--assets', UI_ASSETS_DIR);
  }

  backendProcess = spawn(BUCKLEY_BIN, args, { stdio: 'inherit' });
  backendProcess.on('exit', (code, signal) => {
    const exitLabel = signal ?? code ?? 'unknown';
    console.log(`[buckley] backend exited (${exitLabel})`);
    backendProcess = undefined;
  });
  backendProcess.on('error', (err) => {
    console.error('[buckley] failed to launch backend', err);
  });
}

function stopBackend() {
  if (!SPAWN_BACKEND) {
    return;
  }
  if (backendProcess && !backendProcess.killed) {
    backendProcess.kill('SIGTERM');
  }
}

let mainWindow;

function parseBind(bind) {
  try {
    const parsed = new URL(`http://${bind}`);
    let host = parsed.hostname;
    if (host === '0.0.0.0') {
      host = '127.0.0.1';
    }
    if (host === '::') {
      host = '::1';
    }
    return {
      host,
      port: parsed.port || '80',
    };
  } catch {
    return { host: '127.0.0.1', port: '4488' };
  }
}

function formatHost(host) {
  if (host.includes(':') && !host.startsWith('[')) {
    return `[${host}]`;
  }
  return host;
}

function normalizeURL(raw) {
  if (!raw) {
    return raw;
  }
  if (!/^https?:\/\//i.test(raw)) {
    return `http://${raw}`;
  }
  return raw;
}

function resolveBuckleyBin(explicitPath) {
  const trimmed = typeof explicitPath === 'string' ? explicitPath.trim() : '';
  if (trimmed) {
    return trimmed;
  }

  const binaryName = process.platform === 'win32' ? 'buckley.exe' : 'buckley';
  const candidates = [
    process.resourcesPath ? path.join(process.resourcesPath, binaryName) : '',
    path.join(__dirname, '..', 'dist', binaryName),
  ];

  for (const candidate of candidates) {
    if (!candidate) {
      continue;
    }
    try {
      if (fs.statSync(candidate).isFile()) {
        return candidate;
      }
    } catch {
      // Ignore missing candidates.
    }
  }

  return binaryName;
}

async function waitForBackend(timeoutMs = 15000) {
  if (!SPAWN_BACKEND) {
    return;
  }

  const start = Date.now();

  await new Promise((resolve) => {
    const tick = () => {
      const req = http.request(
        {
          method: 'GET',
          hostname: bindInfo.host,
          port: bindInfo.port,
          path: '/healthz',
          timeout: 1000,
        },
        (res) => {
          res.resume();
          if (res.statusCode && res.statusCode >= 200 && res.statusCode < 500) {
            resolve();
            return;
          }
          schedule();
        }
      );

      req.on('error', schedule);
      req.on('timeout', () => {
        req.destroy();
        schedule();
      });
      req.end();
    };

    const schedule = () => {
      if (Date.now() - start > timeoutMs) {
        resolve();
        return;
      }
      setTimeout(tick, 300);
    };

    tick();
  });
}

async function createWindow() {
  mainWindow = new BrowserWindow({
    width: 1400,
    height: 900,
    minWidth: 1024,
    minHeight: 720,
    backgroundColor: '#05070f',
    title: 'Buckley Desktop',
    webPreferences: {
      contextIsolation: true,
    },
  });

  if (SPAWN_BACKEND) {
    await waitForBackend();
  }

  if (isDev) {
    await mainWindow.loadURL(UI_URL);
    mainWindow.webContents.openDevTools({ mode: 'detach' });
  } else {
    await mainWindow.loadURL(UI_URL);
  }
}

app.on('ready', async () => {
  startBackend();
  await createWindow();
});

app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') {
    app.quit();
  }
});

app.on('before-quit', () => {
  stopBackend();
});

app.on('activate', async () => {
  if (BrowserWindow.getAllWindows().length === 0) {
    await createWindow();
  }
});

process.on('exit', stopBackend);
process.on('SIGINT', () => {
  stopBackend();
  process.exit(0);
});
process.on('SIGTERM', () => {
  stopBackend();
  process.exit(0);
});
