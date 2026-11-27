import { parseArgs } from 'node:util';
import type { AttachOptions } from './common.js';

process.noAsar = true;

async function main(): Promise<void> {
  const { values } = parseArgs({
    options: {
      discover: { type: 'boolean' },
      attach: { type: 'boolean' },
      bundle: { type: 'string' },
    },
    strict: true,
  });
  if (
    Boolean(values.discover) === Boolean(values.attach) ||
    (values.attach && !values.bundle)
  ) {
    throw new Error('Use --discover or --attach --bundle <path>');
  }
  if (process.platform !== 'darwin' && process.platform !== 'win32')
    throw new Error('PLATFORM_UNSUPPORTED');
  const bootstrapper =
    process.platform === 'darwin'
      ? await import('./darwin.js')
      : await import('./windows.js');
  const identity = await bootstrapper.discover();
  if (values.discover) {
    process.stdout.write(`${JSON.stringify(identity)}\n`);
    return;
  }
  const chunks: Buffer[] = [];
  let length = 0;
  for await (const chunk of process.stdin) {
    length += chunk.length;
    if (length > 65_536) throw new Error('BOOTSTRAP_INPUT_TOO_LARGE');
    chunks.push(Buffer.from(chunk));
  }
  const options: AttachOptions = JSON.parse(Buffer.concat(chunks).toString());
  const endpoint = await bootstrapper.attach(identity, values.bundle!, options);
  process.stdout.write(`${JSON.stringify(endpoint)}\n`);
}

main().catch((error: unknown) => {
  process.stderr.write(
    `${error instanceof Error ? error.message : String(error)}\n`,
  );
  process.exitCode = 1;
});
