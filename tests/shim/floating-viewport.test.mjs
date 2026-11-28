import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { build } from "../../web/node_modules/esbuild/lib/main.js";
import { chromium } from "../../web/node_modules/playwright/index.mjs";

const bundle = await build({
  stdin: {
    contents:
      "import {installMobileRuntime} from './browser/mobile-runtime';installMobileRuntime();",
    resolveDir: new URL("../../web/", import.meta.url).pathname,
    loader: "ts",
  },
  bundle: true,
  platform: "browser",
  format: "iife",
  write: false,
});
const css = await readFile(
  new URL("../../web/browser/mobile-layout.css", import.meta.url),
  "utf8",
);

async function setup(t, { mobile = true, zoom = 1 } = {}) {
  const server = createServer((_request, response) => {
    response.setHeader("Content-Type", "text/html; charset=utf-8");
    response.end(`<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><style>${css}
      body{margin:0;zoom:${zoom}}[role=menu]{box-sizing:border-box;background:white;border:1px solid;min-width:180px}
      [role=menu] button{display:block;height:44px}#parent [role=menu]{min-width:158px}
      </style><div id="parent" data-radix-popper-content-wrapper style="position:fixed;left:0;top:0;transform:translate(148px,660px)"><div role="menu"><button>Actions</button><button>Retry</button></div></div>
      <div id="child" data-radix-popper-content-wrapper style="position:fixed;left:0;top:0;transform:translate(-37px,616px);min-width:max-content;translate:7px 3px"><div role="menu"><button id="choose">Choose</button><button>Second action</button></div></div>
      <script>${bundle.outputFiles[0].text}</script>`);
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  t.after(() => new Promise((resolve) => server.close(resolve)));
  const browser = await chromium.launch({ headless: true });
  t.after(() => browser.close());
  const page = await browser.newPage({
    viewport: { width: 411, height: 810 },
    ...(mobile
      ? {
          userAgent:
            "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 Chrome/157.0.0.0 Mobile Safari/537.36",
          hasTouch: true,
        }
      : {}),
  });
  await page.goto("http://127.0.0.1:" + server.address().port);
  return page;
}

async function assertInside(page) {
  await page.waitForFunction(() => {
    const viewport = window.visualViewport;
    return (
      [
        ...document.querySelectorAll(
          "[data-cwb-floating],[data-cwb-floating-content]",
        ),
      ].every((element) => {
        const rect = element.getBoundingClientRect();
        return (
          rect.left >= 7.99 &&
          rect.top >= 7.99 &&
          rect.right <= viewport.width - 7.99 &&
          rect.bottom <= viewport.height - 7.99
        );
      }) && document.querySelectorAll("[data-cwb-floating]").length > 0
    );
  });
}

test("mobile nested menus stay inside the viewport while preserving author positioning and interaction", async (t) => {
  const page = await setup(t);
  await assertInside(page);
  assert.deepEqual(
    await page.locator("#child").evaluate((element) => ({
      transform: element.style.transform,
      translate: element.style.translate,
    })),
    { transform: "translate(-37px, 616px)", translate: "7px 3px" },
  );
  await page
    .locator("#choose")
    .evaluate((element) =>
      element.addEventListener(
        "click",
        () => (document.body.dataset.chosen = "true"),
      ),
    );
  await page.locator("#choose").click();
  assert.equal(await page.locator("body").getAttribute("data-chosen"), "true");
  await page
    .locator("#parent")
    .evaluate(
      (element) => (element.style.transform = "translate(390px,-40px)"),
    );
  await assertInside(page);
  await page.setViewportSize({ width: 320, height: 420 });
  await assertInside(page);
});

test("mobile clamping accounts for CSS zoom and restores closed popover styles", async (t) => {
  const page = await setup(t, { zoom: 1.25 });
  await assertInside(page);
  await page.evaluate(() => {
    document.querySelector("#parent").remove();
    document.querySelector("#child").remove();
    const popover = document.createElement("div");
    popover.id = "native";
    popover.popover = "manual";
    popover.role = "menu";
    popover.style.cssText =
      "position:fixed;margin:0;left:-80px;top:700px;width:600px;height:1200px;translate:4px 2px";
    for (let index = 0; index < 24; index++) {
      const item = document.createElement("button");
      item.textContent = "Action " + index;
      popover.append(item);
    }
    document.body.append(popover);
    popover.showPopover();
  });
  await assertInside(page);
  assert.equal(
    await page
      .locator("#native")
      .evaluate((element) => element.scrollHeight > element.clientHeight),
    true,
  );
  await page.locator("#native button").last().click();
  await assertInside(page);
  await page.locator("#native").evaluate((element) => element.hidePopover());
  await page.waitForFunction(
    () => !document.querySelector("#native").hasAttribute("data-cwb-floating"),
  );
  assert.equal(
    await page
      .locator("#native")
      .evaluate((element) => element.style.translate),
    "4px 2px",
  );
});

test("desktop floating menu geometry stays under the positioning library control", async (t) => {
  const page = await setup(t, { mobile: false });
  assert.equal(
    await page
      .locator("#child")
      .evaluate((element) => element.getBoundingClientRect().left),
    -30,
  );
  assert.equal(await page.locator("[data-cwb-floating]").count(), 0);
});

test("composer attachment overlays stay inside the viewport and remain scrollable", async (t) => {
  const page = await setup(t);
  await page.evaluate(() => {
    document.querySelector("#parent").remove();
    document.querySelector("#child").remove();
    const floating = document.createElement("div");
    floating.id = "attachments";
    floating.style.cssText =
      "position:fixed;left:380px;top:800px;width:480px;height:1000px;transform:translateY(-100%)";
    const overlay = document.createElement("div");
    overlay.dataset.composerOverlayFloatingUi = "true";
    for (let index = 0; index < 24; index++) {
      const button = document.createElement("button");
      button.textContent = "Attachment " + index;
      button.style.cssText = "display:block;height:44px";
      button.addEventListener("click", () => {
        document.body.dataset.attachment = String(index);
      });
      overlay.append(button);
    }
    floating.append(overlay);
    document.body.append(floating);
  });
  await assertInside(page);
  assert.equal(
    await page.locator("#attachments").getAttribute("data-cwb-floating"),
    "true",
  );
  await page.locator("#attachments button").last().click();
  assert.equal(
    await page.locator("body").getAttribute("data-attachment"),
    "23",
  );
  await page.setViewportSize({ width: 320, height: 420 });
  await assertInside(page);
});

test("home mode buttons retain usable touch targets in a narrow viewport", async (t) => {
  const page = await setup(t);
  await page.setViewportSize({ width: 320, height: 640 });
  await page.evaluate(() => {
    document.querySelector("#parent").remove();
    document.querySelector("#child").remove();
    const group = document.createElement("div");
    group.role = "group";
    group.setAttribute("aria-label", "撰写器模式");
    group.style.cssText =
      "display:grid;grid-template-columns:1fr 1fr;width:112px;height:36px;margin:0 auto";
    for (const text of ["聊天", "工作"]) {
      const button = document.createElement("button");
      button.textContent = text;
      button.style.height = "100%";
      group.append(button);
    }
    document.body.append(group);
  });
  const buttons = await page
    .locator("[role=group] button")
    .evaluateAll((elements) =>
      elements.map((element) => element.getBoundingClientRect().toJSON()),
    );
  assert.equal(buttons.length, 2);
  for (const rect of buttons) {
    assert.ok(rect.height >= 44 && rect.width >= 44);
    assert.ok(rect.left >= 0 && rect.right <= 320);
  }
});
