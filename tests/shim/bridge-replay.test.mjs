import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import vm from 'node:vm';
import { build } from '../../web/node_modules/esbuild/lib/main.js';

const bundle = await build({
  stdin: {
    contents: "export {BridgeReplay} from './browser/bridge-replay';",
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
  Uint8Array,
  DataView,
  Error,
  Map,
  Number,
});
const { BridgeReplay } = module.exports;
const payload = new Uint8Array(
  await readFile(
    new URL('../../web/browser/browser-transport.ts', import.meta.url),
  ),
);

test('bridge replay preserves pending bytes and delivers each sequence once after reconnect', () => {
  const sender = new BridgeReplay(),
    receiver = new BridgeReplay();
  const first = sender.enqueue(payload),
    second = sender.enqueue(payload);
  let deliveries = 0;
  const deliver = (data) => {
    assert.deepEqual(data, payload);
    deliveries++;
  };
  receiver.receive(first.buffer, deliver);
  receiver.receive(first.buffer, deliver);
  receiver.receive(second.buffer, deliver);
  assert.equal(deliveries, 2);
  assert.equal(receiver.received, 2);
  assert.equal([...sender.replay()].length, 2);
  sender.acknowledge(1);
  assert.equal([...sender.replay()][0], second);
  sender.acknowledge(2);
  assert.equal([...sender.replay()].length, 0);
  assert.throws(() => sender.acknowledge(3), /ACK_INVALID/);
  assert.throws(() => sender.acknowledge(-1), /ACK_INVALID/);
  const gap = new BridgeReplay();
  assert.throws(() => gap.receive(second.buffer, deliver), /SEQUENCE_INVALID/);
  assert.equal(gap.received, 0);
});
