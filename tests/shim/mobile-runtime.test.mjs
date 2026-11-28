import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
import { build } from '../../web/node_modules/esbuild/lib/main.js';
import { chromium } from '../../web/node_modules/playwright/index.mjs';

const bundle = await build({
  stdin: {
    contents:
      "import {installMobileRuntime} from './browser/mobile-runtime'; installMobileRuntime();",
    resolveDir: new URL('../../web/', import.meta.url).pathname,
    loader: 'ts',
  },
  bundle: true,
  platform: 'browser',
  format: 'iife',
  write: false,
});

async function pickerPage(t, android) {
  const html = `<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1">
    <label for="attachment">Attach files</label>
    <input type="file" id="attachment" aria-label="Attach files" accept="image/*" capture="environment" multiple>
    <input type="file" id="camera" aria-label="Take a photo" accept="image/*" capture="environment">
    <input type="file" id="photos" aria-label="附加照片或视频" accept="image/*,video/*" multiple>
    <button id="choose">Choose</button>
    <script>${bundle.outputFiles[0].text}</script>`;
  const server = createServer((_, response) => {
    response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    response.end(html);
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  t.after(() => new Promise((resolve) => server.close(resolve)));
  const browser = await chromium.launch({ headless: true });
  t.after(() => browser.close());
  const context = await browser.newContext(
    android
      ? {
          userAgent:
            'Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/157.0.0.0 Mobile Safari/537.36',
        }
      : {},
  );
  const page = await context.newPage();
  await page.goto(`http://127.0.0.1:${server.address().port}/`);
  return page;
}

test('Android document picker prepares click, base click, showPicker and label activation', async (t) => {
  const page = await pickerPage(t, true);
  const filename = new URL('../../README.md', import.meta.url).pathname;
  const source = await readFile(filename, 'utf8');
  for (const method of ['click', 'base', 'showPicker', 'label']) {
    await page.evaluate((method) => {
      const input = document.querySelector('#attachment');
      input.accept = 'image/*';
      input.setAttribute('capture', 'environment');
      input.value = '';
      document.querySelector('#choose').onclick = () => {
        if (method === 'base') HTMLElement.prototype.click.call(input);
        else if (method === 'showPicker') input.showPicker();
        else input.click();
      };
    }, method);
    const [chooser] = await Promise.all([
      page.waitForEvent('filechooser'),
      page.locator(method === 'label' ? 'label' : '#choose').click(),
    ]);
    assert.deepEqual(
      await chooser.element().evaluate((input) => ({
        accept: input.accept,
        capture: input.getAttribute('capture'),
        multiple: input.multiple,
        label: input.getAttribute('aria-label'),
        id: input.id,
      })),
      {
        accept: 'application/*,text/*',
        capture: null,
        multiple: true,
        label: 'Attach files',
        id: 'attachment',
      },
    );
    await chooser.setFiles(filename);
    assert.equal(
      await page
        .locator('#attachment')
        .evaluate((input) => input.files[0].text()),
      source,
    );
  }
});

test('Android camera keeps its image capture attributes', async (t) => {
  const page = await pickerPage(t, true);
  await page.evaluate(() => {
    document.querySelector('#choose').onclick = () =>
      document.querySelector('#camera').click();
  });
  const [chooser] = await Promise.all([
    page.waitForEvent('filechooser'),
    page.locator('#choose').click(),
  ]);
  assert.equal(await chooser.element().getAttribute('accept'), 'image/*');
  assert.equal(await chooser.element().getAttribute('capture'), 'environment');
  await chooser.setFiles([]);
});

test('Android dedicated photo and video pickers retain their media filters', async (t) => {
  const page = await pickerPage(t, true);
  for (const item of [
    { label: '附加照片或视频', accept: 'image/*,video/*' },
    { label: '添加照片', accept: 'image/*' },
    { label: 'Attach photos', accept: 'image/png, image/jpeg' },
    { label: 'Attach photos or videos', accept: 'image/*,video/mp4' },
  ]) {
    for (const method of ['click', 'base', 'showPicker']) {
      await page.evaluate(
        ({ item, method }) => {
          const input = document.querySelector('#photos');
          input.accept = item.accept;
          input.setAttribute('aria-label', item.label);
          document.querySelector('#choose').onclick = () => {
            if (method === 'base') HTMLElement.prototype.click.call(input);
            else if (method === 'showPicker') input.showPicker();
            else input.click();
          };
        },
        { item, method },
      );
      const [chooser] = await Promise.all([
        page.waitForEvent('filechooser'),
        page.locator('#choose').click(),
      ]);
      assert.equal(await chooser.element().getAttribute('accept'), item.accept);
      assert.equal(await chooser.element().getAttribute('capture'), null);
      assert.equal(chooser.isMultiple(), true);
      await chooser.setFiles([]);
    }
  }
});

test('Android mixed document inputs retain access to documents despite a media label', async (t) => {
  const page = await pickerPage(t, true);
  await page.evaluate(() => {
    const input = document.querySelector('#photos');
    input.accept = 'image/*,text/plain';
    document.querySelector('#choose').onclick = () => input.click();
  });
  const [chooser] = await Promise.all([
    page.waitForEvent('filechooser'),
    page.locator('#choose').click(),
  ]);
  assert.equal(
    await chooser.element().getAttribute('accept'),
    'application/*,text/*',
  );
  await chooser.setFiles([]);
});

test('desktop picker retains the original native attributes', async (t) => {
  const page = await pickerPage(t, false);
  const [chooser] = await Promise.all([
    page.waitForEvent('filechooser'),
    page.locator('label').click(),
  ]);
  assert.equal(await chooser.element().getAttribute('accept'), 'image/*');
  assert.equal(await chooser.element().getAttribute('capture'), 'environment');
  await chooser.setFiles([]);
});
