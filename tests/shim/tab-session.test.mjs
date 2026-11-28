import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { test } from 'node:test';
import { build } from '../../web/node_modules/esbuild/lib/main.js';
import { chromium } from '../../web/node_modules/playwright/index.mjs';
const bundle = await build({
  stdin: {
    contents:
      "import {initializeTabSession} from './browser/tab-session';initializeTabSession();",
    resolveDir: new URL('../../web/', import.meta.url).pathname,
    loader: 'ts',
  },
  bundle: true,
  platform: 'browser',
  format: 'iife',
  write: false,
});
test('tab session survives same-tab navigation and isolates a new tab with copied sessionStorage', async (t) => {
  const server = createServer((_request, response) => {
    response.setHeader('Content-Type', 'text/html');
    response.end(
      '<!doctype html><script>' + bundle.outputFiles[0].text + '</script>',
    );
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise((resolve) => server.close(resolve)));
  const browser = await chromium.launch({ headless: true });
  t.after(() => browser.close());
  const page = await browser.newPage();
  const origin = 'http://127.0.0.1:' + server.address().port;
  await page.goto(origin);
  const tab = await page.evaluate(() => window.name);
  await page.evaluate(() =>
    sessionStorage.setItem('cwb-owned-claim', crypto.randomUUID()),
  );
  const claim = await page.evaluate(() =>
    sessionStorage.getItem('cwb-owned-claim'),
  );
  await page.goto(origin + '/next');
  assert.equal(await page.evaluate(() => window.name), tab);
  assert.equal(
    await page.evaluate(() => sessionStorage.getItem('cwb-owned-claim')),
    claim,
  );
  const popupEvent = page.waitForEvent('popup');
  await page.evaluate(() => window.open('/new-tab', '_blank'));
  const popup = await popupEvent;
  await popup.waitForLoadState();
  assert.notEqual(await popup.evaluate(() => window.name), tab);
  assert.equal(
    await popup.evaluate(() => sessionStorage.getItem('cwb-owned-claim')),
    null,
  );
  assert.equal(
    await page.evaluate(() => sessionStorage.getItem('cwb-owned-claim')),
    claim,
  );
});
