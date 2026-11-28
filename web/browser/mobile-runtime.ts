import { installFloatingViewport } from "./floating-viewport";

export const mobileBrowser =
  /Android|iPhone|iPad|iPod/i.test(navigator.userAgent) ||
  (navigator.maxTouchPoints > 1 && /Macintosh/.test(navigator.userAgent));

export function isTextEditable(element: unknown): boolean {
  if (element instanceof HTMLTextAreaElement) return true;
  if (element instanceof HTMLInputElement)
    return ![
      "button",
      "checkbox",
      "radio",
      "file",
      "hidden",
      "image",
      "range",
      "reset",
      "submit",
      "color",
    ].includes(element.type);
  return (
    element instanceof HTMLElement &&
    (element.isContentEditable ||
      ["", "true", "plaintext-only"].includes(
        element.getAttribute("contenteditable") ?? "false",
      ))
  );
}

function installMobileFocusGuard() {
  const nativeFocus = HTMLElement.prototype.focus;
  const nativeBlur = HTMLElement.prototype.blur;
  const composerSelector =
    "[data-codex-composer], [data-codex-composer-root], [data-composer-surface-variant]";
  const controlSelector =
    "button, a, label, select, input, summary, [role=button], [role=link], [role=menu], [role=menuitem], [role=listbox], [role=option], [role=combobox], [role=tab], [role=checkbox], [role=radio], [role=switch], [role=slider], [contenteditable=false]";
  let intent: HTMLElement | null = null;
  let intentAt = -Infinity;
  let tabUntil = -Infinity;
  let focusing: HTMLElement | null = null;
  function clearIntent() {
    intent = null;
    intentAt = tabUntil = -Infinity;
  }
  function intended(element: HTMLElement) {
    if (performance.now() <= tabUntil) return true;
    if (!intent || !intent.isConnected || performance.now() - intentAt > 600)
      return false;
    if (isTextEditable(intent))
      return (
        intent === element ||
        element.contains(intent) ||
        intent.contains(element)
      );
    return intent.matches(composerSelector) && intent.contains(element);
  }
  function dismiss() {
    clearIntent();
    if (
      document.activeElement instanceof HTMLElement &&
      isTextEditable(document.activeElement)
    )
      nativeBlur.call(document.activeElement);
  }
  function interaction(event: Event) {
    if (!event.isTrusted) return;
    const target = event
      .composedPath()
      .find((item) => item instanceof HTMLElement);
    if (!(target instanceof HTMLElement)) {
      dismiss();
      return;
    }
    let editable: HTMLElement | null = target;
    while (editable && !isTextEditable(editable))
      editable = editable.parentElement;
    const label = target.closest("label");
    const control = target.closest(controlSelector);
    const composer = target.closest<HTMLElement>(composerSelector);
    const next =
      editable && (!control || control === editable)
        ? editable
        : label &&
            isTextEditable(label.control) &&
            (!control || control === label || control === label.control)
          ? label.control
          : composer && !control
            ? composer
            : null;
    if (!next) {
      dismiss();
      return;
    }
    intent = next;
    intentAt = performance.now();
    tabUntil = -Infinity;
  }
  for (const type of ["pointerdown", "touchstart", "click"])
    document.addEventListener(type, interaction, true);
  document.addEventListener(
    "keydown",
    (event) => {
      if (!event.isTrusted) return;
      clearIntent();
      if (
        event.key === "Tab" &&
        !event.altKey &&
        !event.ctrlKey &&
        !event.metaKey
      )
        tabUntil = performance.now() + 600;
    },
    true,
  );
  HTMLElement.prototype.focus = function (options?: FocusOptions) {
    if (!isTextEditable(this)) return nativeFocus.call(this, options);
    if (
      !(
        document.activeElement === this || this.contains(document.activeElement)
      ) &&
      !intended(this)
    )
      return;
    const previous = focusing;
    focusing = this;
    try {
      return nativeFocus.call(this, options);
    } finally {
      focusing = previous;
    }
  };
  // focusin 已发生焦点切换，使用授权记录判断原生 autofocus。
  document.addEventListener(
    "focusin",
    (event) => {
      const target = event.target;
      if (!(target instanceof HTMLElement) || !isTextEditable(target)) return;
      if (focusing === target || intended(target)) {
        tabUntil = -Infinity;
        return;
      }
      nativeBlur.call(target);
    },
    true,
  );
  document.addEventListener(
    "focusout",
    (event) => {
      if (
        !(event.target instanceof HTMLElement) ||
        !isTextEditable(event.target)
      )
        return;
      if (
        event.relatedTarget instanceof HTMLElement &&
        isTextEditable(event.relatedTarget) &&
        intended(event.relatedTarget)
      )
        return;
      if (
        intent === event.target ||
        (intent &&
          (event.target.contains(intent) || intent.contains(event.target)))
      )
        clearIntent();
    },
    true,
  );
  window.addEventListener("blur", dismiss);
  document.addEventListener("visibilitychange", dismiss, true);
  return dismiss;
}

export function installMobileRuntime() {
  if (!mobileBrowser) return;
  installFloatingViewport();
  const dismissFocus = installMobileFocusGuard();
  const element = document.documentElement;
  element.dataset.cwbMobile = "true";
  const viewport = window.visualViewport;
  let maximumHeight = window.innerHeight;
  let previousWidth = window.innerWidth;
  let frame = 0;
  function syncViewport() {
    frame = 0;
    // 浏览器缩放时保留手势控制，键盘布局只处理正常缩放比例。
    if (viewport && Math.abs(viewport.scale - 1) > 0.01) return;
    const height = viewport?.height ?? window.innerHeight;
    const widthChanged = Math.abs(window.innerWidth - previousWidth) > 1;
    if (widthChanged) maximumHeight = window.innerHeight;
    previousWidth = window.innerWidth;
    maximumHeight = Math.max(maximumHeight, window.innerHeight, height);
    const editing = isTextEditable(document.activeElement);
    const keyboard = !!editing && maximumHeight - height > 100;
    if (
      element.dataset.cwbKeyboard === "true" &&
      !keyboard &&
      editing &&
      !widthChanged
    )
      dismissFocus();
    element.dataset.cwbKeyboard = String(keyboard);
    element.style.setProperty("--cwb-vvh", `${height}px`);
    element.style.setProperty("--cwb-vv-top", `${viewport?.offsetTop ?? 0}px`);
    if (window.scrollX || window.scrollY) window.scrollTo(0, 0);
  }
  function schedule() {
    if (!frame) frame = requestAnimationFrame(syncViewport);
  }
  viewport?.addEventListener("resize", schedule);
  viewport?.addEventListener("scroll", schedule);
  window.addEventListener("resize", schedule);
  window.addEventListener("scroll", schedule, { passive: true });
  document.addEventListener("focusin", schedule);
  document.addEventListener("focusout", schedule);
  syncViewport();

  if (!/Android/i.test(navigator.userAgent)) return;
  function prepare(input: HTMLInputElement) {
    if (input.type !== "file") return;
    const label = input.getAttribute("aria-label") ?? "";
    const camera =
      input.hasAttribute("capture") &&
      /拍照|相机|camera|take (?:a )?photo/i.test(label);
    const photos =
      !input.hasAttribute("capture") &&
      /照片|图片|相册|photo|media/i.test(label) &&
      input.accept
        .split(",")
        .every((type) => /^(image|video)\/(\*|[\w.+-]+)$/i.test(type.trim()));
    if (camera || photos) return;
    // Android 浏览器通过文档 MIME 类型进入文件管理器。
    input.setAttribute("accept", "application/*,text/*");
    input.removeAttribute("capture");
  }
  const click = HTMLElement.prototype.click;
  HTMLElement.prototype.click = function () {
    if (this instanceof HTMLInputElement) prepare(this);
    return click.call(this);
  };
  const showPicker = HTMLInputElement.prototype.showPicker;
  if (showPicker)
    HTMLInputElement.prototype.showPicker = function () {
      prepare(this);
      return showPicker.call(this);
    };
  // 捕获标签激活及用户直接点击，保留原始事件和 React onChange。
  document.addEventListener(
    "click",
    (event) => {
      const target = event.target;
      if (!(target instanceof Element)) return;
      if (target instanceof HTMLInputElement) prepare(target);
      const label = target.closest("label");
      if (label?.control instanceof HTMLInputElement) prepare(label.control);
    },
    true,
  );
}
