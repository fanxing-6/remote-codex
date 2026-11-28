import assert from 'node:assert/strict';
import { randomBytes } from 'node:crypto';
import { test } from 'node:test';
import vm from 'node:vm';
import { build } from '../../web/node_modules/esbuild/lib/main.js';
const bundle = await build({
  stdin: {
    contents: "export {isGatewayDILRunner} from './browser/dil-runtime';",
    resolveDir: new URL('../../web/', import.meta.url).pathname,
    loader: 'ts',
  },
  bundle: true,
  platform: 'node',
  format: 'cjs',
  write: false,
});
const module = { exports: {} };
vm.runInNewContext(bundle.outputFiles[0].text, {
  module,
  exports: module.exports,
});
const { isGatewayDILRunner } = module.exports;
test('same-origin DIL exception authorizes only the exact session sandbox document', () => {
  const origin = 'https://chat.example.com',
    base = '/cwb/dil/' + randomBytes(32).toString('hex') + '/',
    path = base + 'deployments/dil/v15/runner.html';
  assert.equal(isGatewayDILRunner(new URL(origin + path), origin, base), true);
  for (const value of [
    origin + '/',
    origin + base + 'assets/dil/module.js',
    origin + path + '?url=https://example.com',
    origin + path + '/extra',
    'https://other.example.com' + path,
    'https://name@chat.example.com' + path,
    origin + base + 'deployments/dil/v16/runner.html',
  ])
    assert.equal(
      isGatewayDILRunner(new URL(value), origin, base),
      false,
      value,
    );
  assert.equal(
    isGatewayDILRunner(new URL(origin + path), origin, undefined),
    false,
  );
});
