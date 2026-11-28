import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { test } from 'node:test';
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
      expected: null,
    },
    {
      html: `<div data-app-action-sidebar-thread-active="true" data-app-action-sidebar-thread-kind="local" data-app-action-sidebar-thread-id="${id}">Thread</div>`,
      current: '/',
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
