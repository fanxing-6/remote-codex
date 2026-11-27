import { spawn } from 'node:child_process';
import { access } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));

async function run(script, args = [], cwd = root) {
  const child = spawn(process.execPath, [script, ...args], {
    cwd,
    stdio: 'inherit',
    windowsHide: true,
  });
  await new Promise((resolve, reject) => {
    child.once('error', reject);
    child.once('close', (code) =>
      code === 0
        ? resolve()
        : reject(new Error(`RUNTIME_INSTALL_FAILED: ${script} (${code})`)),
    );
  });
}

await run(`${root}node_modules/electron/install.js`);
const binding = `${root}node_modules/frida/build/frida_binding.node`;
const exists = await access(binding).then(
  () => true,
  (error) => {
    if (error.code !== 'ENOENT') throw error;
    return false;
  },
);
if (!exists) {
  await run(
    `${root}node_modules/prebuild-install/bin.js`,
    ['--runtime=napi', '--target=8'],
    `${root}node_modules/frida`,
  );
}
await access(binding);
