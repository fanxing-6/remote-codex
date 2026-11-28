import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { test } from 'node:test';
import { createServer } from 'node:http';
import { build } from '../../web/node_modules/esbuild/lib/main.js';
import { chromium } from '../../web/node_modules/playwright/index.mjs';

const bundle = await build({
  stdin: {
    contents:
      "import * as routes from './browser/route-state'; window.routeState=routes;",
    resolveDir: new URL('../../web/', import.meta.url).pathname,
    loader: 'ts',
  },
  bundle: true,
  platform: 'browser',
  format: 'iife',
  write: false,
});

test('router notifications preserve local, chat and new-chat routes across reloads', async (t) => {
  const server = createServer((_request, response) =>
    response.end('<!doctype html><title>Route</title>'),
  );
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise((resolve) => server.close(resolve)));
  const browser = await chromium.launch({ headless: true });
  t.after(() => browser.close());
  const page = await browser.newPage();
  await page.goto('http://127.0.0.1:' + server.address().port);
  const id = randomUUID();
  for (const route of [
    '/local/' + id,
    '/c/' + id,
    '/local/' + id,
    '/',
    '/c/' + id,
  ]) {
    await page.addScriptTag({ content: bundle.outputFiles[0].text });
    await page.evaluate(
      (route) => window.routeState.rememberConversationRoute(route),
      route,
    );
    assert.equal(new URL(page.url()).searchParams.get('initialRoute'), route);
    await page.reload();
    assert.equal(new URL(page.url()).searchParams.get('initialRoute'), route);
  }
  await page.addScriptTag({ content: bundle.outputFiles[0].text });
  await page.evaluate(
    (route) => window.routeState.rememberConversationRoute(route),
    '/g/project/c/' + id,
  );
  assert.equal(
    new URL(page.url()).searchParams.get('initialRoute'),
    '/c/' + id,
  );
});

test('route restoration reads visible conversations and preserves local thread navigation', async (t) => {
  const browser = await chromium.launch({ headless: true });
  t.after(() => browser.close());
  const page = await browser.newPage();
  const id = randomUUID();
  for (const item of [
    {
      html: `<div hidden data-above-composer-conversation-id="chatgpt:local-chatgpt:${id}"></div>`,
      current: '/',
      expected: null,
    },
    {
      html: `<div style="display:none" data-chatgpt-selection-conversation-id="${id}"></div>`,
      current: '/',
      expected: null,
    },
    {
      html: `<div data-above-composer-conversation-id="chatgpt:local-chatgpt:${id}">Conversation</div>`,
      current: '/',
      expected: null,
    },
    {
      html: `<div data-chatgpt-selection-conversation-id="${id}">Conversation</div>`,
      current: '/',
      expected: '/c/' + id,
    },
    {
      html: `<div data-chatgpt-selection-conversation-id="chatgpt:conversation:${id}">Conversation</div>`,
      current: '/',
      expected: '/c/' + id,
    },
    {
      html: `<div data-chatgpt-selection-conversation-id="${id}">Conversation</div>`,
      current: '/local/' + id,
      expected: '/c/' + id,
    },
    {
      html: `<div data-app-action-sidebar-thread-active="true" data-app-action-sidebar-thread-kind="local" data-app-action-sidebar-thread-id="${id}">Thread</div>`,
      current: '/c/' + id,
      expected: '/local/' + id,
    },
    {
      html: `<div data-app-action-sidebar-thread-active="true" data-app-action-sidebar-thread-kind="local" data-app-action-sidebar-thread-id="${id}">Thread</div><div hidden data-chatgpt-selection-conversation-id="${id}"></div>`,
      current: '/local/' + id,
      expected: '/local/' + id,
    },
  ]) {
    await page.setContent(item.html);
    await page.addScriptTag({ content: bundle.outputFiles[0].text });
    assert.equal(
      await page.evaluate(
        (current) =>
          window.routeState.visibleConversationRoute(document, current),
        item.current,
      ),
      item.expected,
    );
  }
  await page.setContent(
    `<div data-sidebar-chatgpt-conversation-key="chatgpt:conversation:${id}"><button>Conversation</button></div>`,
  );
  await page.addScriptTag({ content: bundle.outputFiles[0].text });
  assert.equal(
    await page
      .locator('button')
      .evaluate((target) => window.routeState.clickedConversationRoute(target)),
    '/c/' + id,
  );
  await page.setContent(
    `<div data-app-action-sidebar-thread-row data-app-action-sidebar-thread-kind="local" data-app-action-sidebar-thread-id="${id}"><button>Thread</button></div>`,
  );
  await page.addScriptTag({ content: bundle.outputFiles[0].text });
  assert.equal(
    await page
      .locator('button')
      .evaluate((target) => window.routeState.clickedConversationRoute(target)),
    '/local/' + id,
  );
  assert.equal(
    await page.evaluate(
      (id) =>
        window.routeState.routeForThreadKey(
          'chatgpt:local-chatgpt:' + id,
          'chatgpt',
        ),
      id,
    ),
    null,
  );
  assert.equal(
    await page.evaluate(
      (id) => window.routeState.routeForThreadKey('codex:thread:local:' + id),
      id,
    ),
    '/local/' + id,
  );
});
