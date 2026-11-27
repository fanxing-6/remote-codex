import { createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parse } from 'ini';
import {
  agentSource,
  assertOptions,
  installExpression,
  portOpen,
  run,
  waitFor,
  withAgent,
  type AttachOptions,
  type ProcessIdentity,
  type ShimEndpoint,
} from './common.js';

export async function discover(): Promise<ProcessIdentity> {
  const script = fileURLToPath(new URL('./discover.ps1', import.meta.url));
  const result = JSON.parse(
    await run('powershell.exe', [
      '-NoLogo',
      '-NoProfile',
      '-NonInteractive',
      '-ExecutionPolicy',
      'Bypass',
      '-File',
      script,
    ]),
  );
  const ini = parse(await readFile(result.iniPath, 'utf8'));
  const versions = Object.values(ini)
    .filter((section) => typeof section === 'object' && section !== null)
    .flatMap((section) => Object.entries(section as Record<string, unknown>))
    .filter(([key]) => key.toLowerCase() === 'appversion')
    .map(([, value]) => value);
  if (versions.length !== 1 || typeof versions[0] !== 'string')
    throw new Error('APP_VERSION_NOT_UNIQUE');
  const { pid, startedAt, executablePath, asarPath } = result;
  return { pid, startedAt, executablePath, asarPath, appVersion: versions[0] };
}

async function listeners(pid: number): Promise<Set<number>> {
  const command = `$ErrorActionPreference='Stop';$ports=@(Get-NetTCPConnection -State Listen | Where-Object OwningProcess -eq ${pid} | Select-Object -ExpandProperty LocalPort -Unique);ConvertTo-Json -InputObject $ports -Compress`;
  const values: number[] = JSON.parse(
    await run('powershell.exe', [
      '-NoProfile',
      '-NonInteractive',
      '-Command',
      command,
    ]),
  );
  return new Set(values);
}

export async function attach(
  identity: ProcessIdentity,
  bundle: string,
  options: AttachOptions,
): Promise<ShimEndpoint> {
  assertOptions(identity, options);
  const hash = createHash('sha256')
    .update(
      await readFile(
        path.join(path.dirname(identity.executablePath), 'chrome.dll'),
      ),
    )
    .digest('hex');
  if (
    hash !== '0db43d9385011630242c68e95c5fe7534e8a71a23e81f06a3be7a83185cc8022'
  )
    throw new Error('RUNTIME_ABI_UNVERIFIED');
  if (await portOpen(9229)) throw new Error('INSPECTOR_ALREADY_OPEN');
  const before = await listeners(identity.pid);
  const begin = performance.now();
  const source = (await agentSource('windows_bootstrap.js')).replace(
    'BOOTSTRAP_SOURCE',
    JSON.stringify(installExpression(bundle, options)),
  );
  await withAgent(identity.pid, source, async (script) => {
    let complete:
      | { executed?: boolean; asyncClosed?: boolean; failure?: string }
      | undefined;
    let agentError = false;
    script.message.connect((message) => {
      if (message.type === 'error') agentError = true;
      if (message.type === 'send' && message.payload.kind === 'complete')
        complete = message.payload;
    });
    await script.load();
    const deadline = performance.now() + 20_000;
    await waitFor(
      async () => {
        if (agentError) throw new Error('FRIDA_AGENT_ERROR');
        if (complete) return true;
        if (performance.now() >= deadline) {
          await script.exports.cancel();
          await waitFor(
            async () => (complete ? true : undefined),
            20_000,
            'MAIN_CALLBACK_CLEANUP_TIMEOUT',
          );
          return true;
        }
        return undefined;
      },
      41_000,
      'NATIVE_EXECUTION_TIMEOUT',
    );
    if (!complete?.executed || !complete.asyncClosed || complete.failure)
      throw new Error(complete?.failure ?? 'NATIVE_EXECUTION_UNCONFIRMED');
  });
  const port = await waitFor(
    async () => {
      const ports = [...(await listeners(identity.pid))].filter(
        (value) => !before.has(value),
      );
      return ports.length === 1 ? ports[0] : undefined;
    },
    5000,
    'IPC_LISTENER_NOT_UNIQUE',
  );
  const current = await discover();
  if (
    (await portOpen(9229)) ||
    current.pid !== identity.pid ||
    current.startedAt !== identity.startedAt
  )
    throw new Error('FINAL_IDENTITY_CHECK_FAILED');
  return {
    ...identity,
    url: `ws://127.0.0.1:${port}/internal`,
    inspectorClosed: true,
    inspectorOpenMs: 0,
    bootstrapElapsedMs: performance.now() - begin,
  };
}
