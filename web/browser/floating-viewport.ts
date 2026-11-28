const floatingSelector =
  '[data-radix-popper-content-wrapper], [data-floating-ui-placement], :has(> [data-composer-overlay-floating-ui]), [popover], [role="menu"], [role="listbox"], [role="tooltip"]';
const wrapperSelector = "[data-radix-popper-content-wrapper]";

export function floatingBounds(viewport: {
  width: number;
  height: number;
  offsetLeft: number;
  offsetTop: number;
}) {
  return {
    left: Math.ceil(viewport.offsetLeft + 8),
    top: Math.ceil(viewport.offsetTop + 8),
    right: Math.floor(viewport.offsetLeft + viewport.width - 8),
    bottom: Math.floor(viewport.offsetTop + viewport.height - 8),
  };
}

export function floatingCorrection(
  rect: Pick<DOMRect, "left" | "top" | "width" | "height">,
  bounds: ReturnType<typeof floatingBounds>,
) {
  return {
    x:
      Math.max(bounds.left, Math.min(rect.left, bounds.right - rect.width)) -
      rect.left,
    y:
      Math.max(bounds.top, Math.min(rect.top, bounds.bottom - rect.height)) -
      rect.top,
  };
}

export function installFloatingViewport() {
  type Floating = {
    animation: Animation;
    content: HTMLElement;
  };
  const active = new Map<HTMLElement, Floating>();
  const ownStyles = new WeakMap<HTMLElement, string | null>();
  const viewport = window.visualViewport;
  let frame = 0;
  function schedule() {
    if (!frame) frame = requestAnimationFrame(update);
  }
  const resize = new ResizeObserver(schedule);
  function limit(element: HTMLElement, width: number, height: number) {
    element.style.setProperty(
      "--cwb-floating-width",
      `${Math.max(1, width)}px`,
    );
    element.style.setProperty(
      "--cwb-floating-height",
      `${Math.max(1, height)}px`,
    );
    ownStyles.set(element, element.getAttribute("style"));
  }
  function clear(element: HTMLElement) {
    delete element.dataset.cwbFloating;
    delete element.dataset.cwbFloatingContent;
    element.style.removeProperty("--cwb-floating-width");
    element.style.removeProperty("--cwb-floating-height");
    ownStyles.set(element, element.getAttribute("style"));
    resize.unobserve(element);
  }
  function offset(animation: Animation, x: number, y: number) {
    const translate = `${x}px ${y}px`;
    (animation.effect as KeyframeEffect).setKeyframes([
      { translate },
      { translate },
    ]);
  }
  function update() {
    frame = 0;
    const bounds = floatingBounds(
      viewport ?? {
        width: window.innerWidth,
        height: window.innerHeight,
        offsetLeft: 0,
        offsetTop: 0,
      },
    );
    const visible = new Set<HTMLElement>();
    for (const element of document.querySelectorAll<HTMLElement>(
      floatingSelector,
    )) {
      if (!element.matches(wrapperSelector) && element.closest(wrapperSelector))
        continue;
      const style = getComputedStyle(element);
      const rect = element.getBoundingClientRect();
      if (
        !["fixed", "absolute"].includes(style.position) ||
        style.visibility === "hidden" ||
        !rect.width ||
        !rect.height
      )
        continue;
      visible.add(element);
    }
    for (const [element, state] of active) {
      if (visible.has(element)) continue;
      state.animation.cancel();
      clear(element);
      if (state.content !== element) clear(state.content);
      active.delete(element);
    }
    for (const element of visible) {
      const content = element.matches(wrapperSelector)
        ? (element.firstElementChild as HTMLElement)
        : element;
      if (!(content instanceof HTMLElement)) continue;
      let state = active.get(element);
      if (!state) {
        // 独立的叠加平移保留定位库的 transform、translate 和动画。
        const animation = element.animate(
          [{ translate: "0px 0px" }, { translate: "0px 0px" }],
          {
            duration: 1,
            fill: "both",
            composite: "add",
          },
        );
        animation.pause();
        animation.currentTime = 0;
        state = { animation, content };
        active.set(element, state);
        element.dataset.cwbFloating = "true";
        resize.observe(element);
      }
      if (state.content !== content) {
        clear(state.content);
        state.content = content;
      }
      content.dataset.cwbFloatingContent = "true";
      resize.observe(content);
      offset(state.animation, 0, 0);
      const origin = element.getBoundingClientRect();
      offset(state.animation, 1, 0);
      const horizontal = element.getBoundingClientRect();
      offset(state.animation, 0, 1);
      const vertical = element.getBoundingClientRect();
      const a = horizontal.left - origin.left;
      const b = horizontal.top - origin.top;
      const c = vertical.left - origin.left;
      const d = vertical.top - origin.top;
      const determinant = a * d - b * c;
      if (Math.abs(determinant) < 0.000001) {
        offset(state.animation, 0, 0);
        continue;
      }
      const width = (bounds.right - bounds.left) / Math.hypot(a, b);
      const height = (bounds.bottom - bounds.top) / Math.hypot(c, d);
      limit(element, width, height);
      if (content !== element) {
        const style = getComputedStyle(element);
        const decoration = [
          "borderTopWidth",
          "borderBottomWidth",
          "paddingTop",
          "paddingBottom",
        ].reduce((sum, key) => sum + parseFloat(style[key]), 0);
        limit(content, width, height - decoration);
      }
      offset(state.animation, 0, 0);
      const correction = floatingCorrection(
        element.getBoundingClientRect(),
        bounds,
      );
      offset(
        state.animation,
        (d * correction.x - c * correction.y) / determinant,
        (a * correction.y - b * correction.x) / determinant,
      );
      if (
        [...element.getAnimations(), ...content.getAnimations()].some(
          (animation) =>
            animation !== state.animation && animation.playState === "running",
        )
      )
        schedule();
    }
  }
  const observer = new MutationObserver((records) => {
    const relevant = records.filter(
      (record) =>
        !(
          record.type === "attributes" &&
          record.attributeName === "style" &&
          record.target instanceof HTMLElement &&
          ownStyles.get(record.target) === record.target.getAttribute("style")
        ),
    );
    if (!relevant.length) return;
    if (
      active.size ||
      relevant.some(
        (record) =>
          (record.target instanceof Element &&
            record.target.matches(floatingSelector)) ||
          [...record.addedNodes].some(
            (node) =>
              node instanceof Element &&
              (node.matches(floatingSelector) ||
                node.querySelector(floatingSelector)),
          ),
      )
    )
      schedule();
  });
  observer.observe(document.documentElement, {
    subtree: true,
    childList: true,
    attributes: true,
    attributeFilter: [
      "style",
      "class",
      "data-state",
      "hidden",
      "open",
      "popover",
    ],
  });
  window.addEventListener("resize", schedule);
  document.addEventListener("scroll", schedule, true);
  document.addEventListener("toggle", schedule, true);
  viewport?.addEventListener("resize", schedule);
  viewport?.addEventListener("scroll", schedule);
  schedule();
  return () => {
    cancelAnimationFrame(frame);
    observer.disconnect();
    resize.disconnect();
    window.removeEventListener("resize", schedule);
    document.removeEventListener("scroll", schedule, true);
    document.removeEventListener("toggle", schedule, true);
    viewport?.removeEventListener("resize", schedule);
    viewport?.removeEventListener("scroll", schedule);
    for (const [element, state] of active) {
      state.animation.cancel();
      clear(element);
      if (state.content !== element) clear(state.content);
    }
    active.clear();
  };
}
