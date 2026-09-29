export type Child = Node | string | number | null | undefined | false;

/**
 * Tiny element builder. Text is always inserted as text nodes (never HTML), so
 * server data cannot inject markup. Props starting with "on" become listeners.
 */
export function h<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  props: Record<string, unknown> = {},
  ...children: Child[]
): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  for (const [key, value] of Object.entries(props)) {
    if (value === undefined || value === null || value === false) continue;
    if (key.startsWith("on") && typeof value === "function") {
      el.addEventListener(key.slice(2).toLowerCase(), value as EventListener);
    } else if (key === "class") {
      el.className = String(value);
    } else if (key in el) {
      (el as unknown as Record<string, unknown>)[key] = value;
    } else {
      el.setAttribute(key, String(value));
    }
  }
  append(el, children);
  return el;
}

export function append(el: Node, children: Child[]): void {
  for (const c of children) {
    if (c === null || c === undefined || c === false) continue;
    el.appendChild(typeof c === "object" ? c : document.createTextNode(String(c)));
  }
}

export function clear(el: Element): void {
  el.replaceChildren();
}

export function formatUptime(seconds: number): string {
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}

export function when(iso: string): string {
  return new Date(iso).toLocaleString();
}

/** Shows a short message in the toast area. */
export function toast(message: string, isError = false): void {
  const area = document.getElementById("toasts");
  if (!area) return;
  const t = h("div", { class: isError ? "toast error" : "toast", role: isError ? "alert" : "status" }, message);
  area.appendChild(t);
  setTimeout(() => t.remove(), isError ? 6000 : 3000);
}

export function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : "Something went wrong";
}
