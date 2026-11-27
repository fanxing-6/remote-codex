import { spawn } from 'node:child_process';
import { readFile } from 'node:fs/promises';
import net from 'node:net';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { setTimeout as delay } from 'node:timers/promises';
import * as frida from 'frida';

export interface ProcessIdentity {
  pid: number;
  startedAt: number;
  executablePath: string;
  appVersion: string;
  asarPath: string;
}

export interface AttachOptions {
  pid: number;
  startedAt: number;
  token: string;
  instance: string;
  appVersion: string;
  assetBuildId: string;
}

export interface ShimEndpoint extends ProcessIdentity {
  url: string;
  inspectorClosed: boolean;
  inspectorOpenMs: number;
  bootstrapElapsedMs: number;
}

export async function run(
  command: string,
  args: string[],
  acceptedCodes = [0],
): Promise<string> {
  const child = spawn(command, args, {
    windowsHide: true,
    stdio: ['ignore', 'pipe', 'pipe'],
    env: { ...process.env, LC_ALL: 'C', TZ: 'UTC' },
    timeout: 15_000,
  });
  const output: Buffer[] = [];
  const errors: Buffer[] = [];
  child.stdout.on('data', (data: Buffer) => output.push(data));
  child.stderr.on('data', (data: Buffer) => errors.push(data));
  await new Promise<void>((resolve, reject) => {
    child.once('error', reject);
    child.once('close', (code) => {
      if (code !== null && acceptedCodes.includes(code)) resolve();
      else
        reject(
          new Error(
            `SYSTEM_COMMAND_FAILED: ${path.basename(command)} (${code}): ${Buffer.concat(errors).toString().trim()}`,
          ),
        );
    });
  });
  return Buffer.concat(output).toString();
}

export function assertOptions(
  identity: ProcessIdentity,
  options: AttachOptions,
): void {
  if (
    identity.appVersion !== '26.1117.41037' ||
    options.appVersion !== identity.appVersion
  ) {
    throw new Error('APP_VERSION_UNVERIFIED');
  }
  if (options.pid !== identity.pid || options.startedAt !== identity.startedAt)
    throw new Error('PROCESS_CHANGED');
  if (
    !/^[a-f0-9]{64}$/.test(options.token) ||
    !/^[a-f0-9]{64}$/.test(options.instance)
  ) {
    throw new Error('BOOTSTRAP_OPTIONS_INVALID');
  }
}

export async function portOpen(port: number): Promise<boolean> {
  return new Promise((resolve, reject) => {
    const socket = net.connect({ port, host: '127.0.0.1' });
    socket.setTimeout(300);
    socket.once('connect', () => {
      socket.destroy();
      resolve(true);
    });
    socket.once('timeout', () => {
      socket.destroy();
      reject(new Error('PORT_PROBE_TIMEOUT'));
    });
    socket.once('error', (error: NodeJS.ErrnoException) => {
      socket.destroy();
      if (error.code === 'ECONNREFUSED') resolve(false);
      else reject(error);
    });
  });
}

export async function waitFor<T>(
  check: () => Promise<T | undefined>,
  timeoutMs: number,
  code: string,
): Promise<T> {
  const deadline = performance.now() + timeoutMs;
  do {
    const value = await check();
    if (value !== undefined) return value;
    await delay(20);
  } while (performance.now() < deadline);
  throw new Error(code);
}

export async function agentSource(name: string): Promise<string> {
  const directory =
    process.env.REMOTE_CODEX_AGENT_DIR ??
    fileURLToPath(new URL('../agents/', import.meta.url));
  return readFile(path.join(directory, name), 'utf8');
}

export async function withAgent<T>(
  pid: number,
  source: string,
  action: (script: frida.Script) => Promise<T>,
): Promise<T> {
  const session = await frida.attach(pid);
  try {
    const script = await session.createScript(source);
    try {
      return await action(script);
    } finally {
      await script.unload();
    }
  } finally {
    await session.detach();
  }
}

export function installExpression(
  bundle: string,
  options: AttachOptions,
): string {
  const filename = JSON.stringify(path.resolve(bundle));
  return `if(process.type!=="browser")throw new Error("MAIN_RUNTIME_INVALID");delete process.getBuiltinModule("module")._cache[${filename}];process.mainModule.require(${filename}).install(${JSON.stringify(options)});true`;
}
