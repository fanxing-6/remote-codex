import assert from 'node:assert/strict';
import { test } from 'node:test';
import { build } from '../../web/node_modules/esbuild/lib/main.js';
import { chromium } from '../../web/node_modules/playwright/index.mjs';

async function bundle(name) {
  const result = await build({
    entryPoints: [
      new URL(`../../web/shim/${name}.ts`, import.meta.url).pathname,
    ],
    bundle: true,
    platform: 'browser',
    format: 'iife',
    target: 'chrome130',
    write: false,
  });
  return result.outputFiles[0].text;
}

const capture = await bundle('early-capture');
const transport = await bundle('host-transport');

test('document_start capture strips scripts and links until DOMContentLoaded', async (t) => {
  const browser = await chromium.launch({
    headless: true,
  });
  t.after(() => browser.close());
  const page = await browser.newPage();
  await page.addInitScript({
    content:
      capture +
      `
      window.blockedElements = [];
      const container = document.createElement('div');
      for (const name of ['script', 'link']) {
        const element = document.createElement(name);
        window.blockedElements.push(element);
        container.append(element);
      }
      document.append(container);
    `,
  });
  await page.goto('about:blank');
  const result = await page.evaluate(async () => {
    const link = document.createElement('link');
    document.documentElement.append(link);
    await Promise.resolve();
    return {
      blocked: window.blockedElements.map((element) => ({
        type: element.getAttribute('type'),
        connected: element.isConnected,
      })),
      captureActive: typeof window.__cwbCapture === 'function',
      observerStopped: link.isConnected,
    };
  });
  assert.deepEqual(result, {
    blocked: [
      { type: 'blocked/cwb', connected: false },
      { type: 'blocked/cwb', connected: false },
    ],
    captureActive: true,
    observerStopped: true,
  });
});

test('host transport releases the capture observer before DOMContentLoaded', async (t) => {
  const browser = await chromium.launch({
    headless: true,
  });
  t.after(() => browser.close());
  const page = await browser.newPage();
  await page.addInitScript({
    content:
      capture +
      '\n' +
      transport +
      `
      window.transportReadyState = document.readyState;
      window.retainedLink = document.createElement('link');
      document.append(window.retainedLink);
    `,
  });
  await page.goto('about:blank');
  assert.deepEqual(
    await page.evaluate(() => ({
      readyState: window.transportReadyState,
      hostReady: !!window.__cwbHost,
      captureRemoved: !('__cwbCapture' in window),
      linkRetained: window.retainedLink.isConnected,
    })),
    {
      readyState: 'loading',
      hostReady: true,
      captureRemoved: true,
      linkRetained: true,
    },
  );
});
