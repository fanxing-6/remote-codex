import { spawn, type ChildProcess } from 'node:child_process';
import { access, mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import { app } from 'electron';

interface RuntimeConfig {
  adminPort: number;
  dataPort: number;
  tunnelMode?: string;
}
interface Credentials {
  id: string;
  csrf: string;
}
export interface SidecarOptions {
  userData: string;
  cloudflaredPath?: string;
  manualHost?: boolean;
}

export class SidecarManager {
  private child?: ChildProcess;
  private exited?: Promise<{
    code: number | null;
    signal: NodeJS.Signals | null;
  }>;
  private stopping?: Promise<void>;
  private credentials?: Credentials;
  private config?: RuntimeConfig;
  private failure?: Error;
  private listeners = new Set<(error: Error) => void>();
  private expectedExit = false;
  private owned = false;

  constructor(private options: SidecarOptions) {}

  get configPath(): string {
    return path.join(this.options.userData, 'config.json');
  }
  get runtimeDir(): string {
    return path.join(this.options.userData, 'runtime');
  }
  get origin(): string {
    if (!this.config) throw new Error('SIDECAR_NOT_READY');
    return `http://127.0.0.1:${this.config.adminPort}`;
  }

  onFailure(listener: (error: Error) => void): void {
    this.listeners.add(listener);
  }

  async refreshConfig(): Promise<void> {
    const config = JSON.parse(
      await readFile(this.configPath, 'utf8'),
    ) as RuntimeConfig;
    if (
      !Number.isInteger(config.adminPort) ||
      config.adminPort < 1024 ||
      config.adminPort > 65535 ||
      !Number.isInteger(config.dataPort) ||
      config.dataPort < 1024 ||
      config.dataPort > 65535 ||
      config.adminPort === config.dataPort
    ) {
      throw new Error('CONFIG_PORT_INVALID');
    }
    this.config = config;
  }

  async start(): Promise<void> {
    if (this.child) throw new Error('SIDECAR_ALREADY_STARTED');
    await mkdir(this.options.userData, { recursive: true, mode: 0o700 });
    const configExists = await access(this.configPath).then(
      () => true,
      (error: NodeJS.ErrnoException) => {
        if (error.code === 'ENOENT') return false;
        throw error;
      },
    );
    if (!configExists) {
      await writeFile(
        this.configPath,
        JSON.stringify(
          {
            tunnelMode: 'quick',
            quickTunnelProtocol: 'http2',
            publicOrigin: '',
            allowedHosts: [],
            dataPort: 18765,
            adminPort: 18766,
            tlsPort: 0,
            tlsAutocert: false,
            auth: { mode: 'password' },
            allowInsecureLocalhost: false,
            trustedProxy: ['127.0.0.1/32'],
          },
          null,
          2,
        ),
        { mode: 0o600, flag: 'wx' },
      );
    }
    await this.refreshConfig();
    const project = fileURLToPath(new URL('../../../', import.meta.url));
    const binary = app.isPackaged
      ? path.join(
          process.resourcesPath,
          'bin',
          process.platform === 'win32' ? 'remote-codex.exe' : 'remote-codex',
        )
      : path.join(
          project,
          'bin',
          process.platform === 'win32'
            ? 'remote-codex-windows-amd64.exe'
            : 'remote-codex',
        );
    const bootstrapper = app.isPackaged
      ? path.join(
          process.resourcesPath,
          'app.asar.unpacked',
          'dist',
          'bootstrapper',
          'index.js',
        )
      : fileURLToPath(new URL('../bootstrapper/index.js', import.meta.url));
    const args = [
      'serve',
      '--no-tray',
      '--config',
      this.configPath,
      '--tunnel-config',
      path.join(this.options.userData, 'tunnel.yml'),
      '--bootstrap-command',
      JSON.stringify([process.execPath, bootstrapper]),
    ];
    if (this.options.manualHost) args.push('--no-host');
    if (this.options.cloudflaredPath)
      args.push('--cloudflared', this.options.cloudflaredPath);
    await access(binary);
    await access(bootstrapper);
    const child = spawn(binary, args, {
      windowsHide: true,
      detached: process.platform !== 'win32',
      stdio: ['ignore', 'pipe', 'pipe'],
      env: {
        ...process.env,
        ELECTRON_RUN_AS_NODE: '1',
        REMOTE_CODEX_BOOTSTRAPPER_NAME: 'Node-Frida',
        REMOTE_CODEX_MANAGED_DESKTOP: '1',
        REMOTE_CODEX_AGENT_DIR: app.isPackaged
          ? path.join(process.resourcesPath, 'agents')
          : fileURLToPath(new URL('../agents/', import.meta.url)),
      },
    });
    this.child = child;
    child.stdout?.on('data', (chunk: Buffer) => process.stdout.write(chunk));
    child.stderr?.on('data', (chunk: Buffer) => process.stderr.write(chunk));
    this.exited = new Promise((resolve) => {
      child.once('error', (error) => {
        this.failure = error;
      });
      child.once('close', (code, signal) => {
        if (!this.expectedExit) {
          const error =
            this.failure ??
            new Error(
              `核心引擎已退出（${code ?? signal}），请确认管理端口可用。`,
            );
          this.failure = error;
          if (this.owned)
            for (const listener of this.listeners) listener(error);
        }
        resolve({ code, signal });
      });
    });
    try {
      const deadline = performance.now() + 20_000;
      while (performance.now() < deadline) {
        if (this.failure) throw this.failure;
        const credentials = await readFile(
          path.join(this.runtimeDir, 'cli-session.json'),
          'utf8',
        ).then(
          (value) => JSON.parse(value) as Credentials,
          (error: NodeJS.ErrnoException) => {
            if (error.code !== 'ENOENT') throw error;
            return undefined;
          },
        );
        if (credentials) {
          this.credentials = credentials;
          const response =
            await this.request<Record<string, unknown>>('status');
          if (response.bootstrapper !== 'Node-Frida')
            throw new Error('BOOTSTRAPPER_RUNTIME_MISMATCH');
          if (response.pid !== child.pid)
            throw new Error('SIDECAR_PROCESS_MISMATCH');
          this.owned = true;
          return;
        }
        await delay(100);
      }
      throw new Error('核心引擎启动超时，请检查端口和配置。');
    } catch (error) {
      await this.stop();
      throw error;
    }
  }

  async request<T>(endpoint: string, body?: unknown): Promise<T> {
    await this.refreshConfig();
    // Go 会定期更新管理会话；每次请求读取当前凭据。
    this.credentials = JSON.parse(
      await readFile(path.join(this.runtimeDir, 'cli-session.json'), 'utf8'),
    ) as Credentials;
    const response = await fetch(`${this.origin}/api/${endpoint}`, {
      method: body === undefined ? 'GET' : 'POST',
      headers: {
        Origin: this.origin,
        'Content-Type': 'application/json',
        Cookie: `cwb_admin=${this.credentials.id}`,
        'X-CSRF-Token': this.credentials.csrf,
      },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(90_000),
    });
    if (!response.ok)
      throw new Error(
        `核心引擎请求失败（${endpoint}: HTTP ${response.status}）`,
      );
    return response.json() as Promise<T>;
  }

  async bootstrapURL(): Promise<string> {
    await this.request('bootstrap/rotate', {});
    const { code } = await this.request<{ code: string }>('bootstrap/code');
    if (!code) throw new Error('BOOTSTRAP_CODE_UNAVAILABLE');
    return `${this.origin}/#bootstrap=${encodeURIComponent(code)}`;
  }

  async loginURL(permanent: boolean): Promise<string> {
    const result = await this.request<{ url: string }>('auth/ticket', {
      permanent,
      ttlSeconds: 300,
    });
    return result.url;
  }

  stop(): Promise<void> {
    this.stopping ??= this.shutdown();
    return this.stopping;
  }

  private async shutdown(): Promise<void> {
    if (!this.child || !this.exited) return;
    this.expectedExit = true;
    if (this.child.exitCode === null && this.child.signalCode === null) {
      if (this.owned) {
        try {
          await this.request('shutdown', {});
        } catch (error) {
          await this.terminateTree();
          throw new Error('核心引擎管理接口关闭失败，已终止所属进程。', {
            cause: error,
          });
        }
      } else if (process.platform !== 'win32') this.child.kill('SIGTERM');
      else this.child.kill();
    }
    let timer: NodeJS.Timeout | undefined;
    try {
      const result = await Promise.race([
        this.exited,
        new Promise<never>((_, reject) => {
          timer = setTimeout(
            () => reject(new Error('核心引擎关闭超时。')),
            30_000,
          );
        }),
      ]);
      if (this.owned && result.code !== 0 && result.signal !== 'SIGTERM')
        throw new Error(
          `核心引擎关闭失败（${result.code ?? result.signal}）。`,
        );
    } catch (error) {
      await this.terminateTree();
      throw error;
    } finally {
      clearTimeout(timer);
    }
  }

  private async terminateTree(): Promise<void> {
    if (!this.child?.pid || !this.exited) return;
    if (process.platform === 'win32') {
      if (this.child.exitCode !== null || this.child.signalCode !== null) {
        await this.exited;
        return;
      }
      const command = spawn(
        'taskkill.exe',
        ['/PID', String(this.child.pid), '/T', '/F'],
        { windowsHide: true, stdio: 'ignore' },
      );
      await new Promise<void>((resolve, reject) => {
        command.once('error', reject);
        command.once('close', (code) =>
          code === 0
            ? resolve()
            : reject(new Error(`PROCESS_CLEANUP_FAILED: ${code}`)),
        );
      });
    } else {
      // 独立进程组包含引擎及其所属的隧道进程。
      try {
        process.kill(-this.child.pid, 'SIGKILL');
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code !== 'ESRCH') throw error;
      }
    }
    await this.exited;
  }
}
