/**
 * TypeScript counterparts of the Astro components in this folder. Views render
 * in the browser, so they compose these builders; they emit the same class names
 * the Astro components do, and both are styled by styles/components/*.css.
 * Visible text is passed in already translated (call t() at the call site).
 */
import { h, type Child } from "../scripts/dom";
import { t } from "../i18n";

/** <section class="card"> with an optional heading. */
export function card(title: Child, ...children: Child[]): HTMLElement {
  return h("section", { class: "card" }, title ? h("h2", {}, title) : null, ...children);
}

/** <form class="card"> for a settings form. */
export function formCard(onsubmit: (ev: Event) => void, ...children: Child[]): HTMLFormElement {
  return h("form", { class: "card", onsubmit }, ...children);
}

export function pageHeader(title: string, description?: string): HTMLElement {
  return h("header", { class: "page-header" }, h("h2", {}, title), description ? note(description) : null);
}

export function note(text: Child): HTMLElement {
  return h("p", { class: "muted" }, text);
}

export function badge(text: string, warn = false): HTMLElement {
  return h("span", { class: warn ? "badge warn" : "badge" }, text);
}

export function alert(text: string, kind: "error" | "info" = "error"): HTMLElement {
  return h("p", { class: kind === "error" ? "alert error" : "alert", role: kind === "error" ? "alert" : "status" }, text);
}

export function emptyState(text: string): HTMLElement {
  return h("p", { class: "empty" }, text);
}

export function loadingState(text: string = t("common.loading")): HTMLElement {
  return h("p", { class: "loading", role: "status" }, text);
}

export function field(label: string, input: HTMLElement): HTMLElement {
  return h("label", { class: "field" }, h("span", {}, label), input);
}

/** A checkbox with its label. */
export function toggle(label: string, input: HTMLInputElement, ...extra: Child[]): HTMLElement {
  return h("label", { class: "check" }, input, ` ${label} `, ...extra);
}

export function checkbox(checked: boolean, props: Record<string, unknown> = {}): HTMLInputElement {
  return h("input", { type: "checkbox", checked, ...props });
}

export function select(aria: string, options: [string, string][], value?: string, props: Record<string, unknown> = {}): HTMLSelectElement {
  return h("select", { ariaLabel: aria, ...props }, ...options.map(([v, label]) => h("option", { value: v, selected: v === value }, label)));
}

export function fields(...children: Child[]): HTMLElement {
  return h("div", { class: "fields" }, ...children);
}

export function actions(...children: Child[]): HTMLElement {
  return h("div", { class: "actions" }, ...children);
}

export function button(label: string, opts: { variant?: "secondary" | "danger" | "ghost"; type?: "button" | "submit"; onclick?: (ev: Event) => unknown; disabled?: boolean } = {}): HTMLButtonElement {
  return h("button", { type: opts.type ?? "button", class: opts.variant, onclick: opts.onclick, disabled: opts.disabled }, label);
}

export function submit(label: string, variant?: "danger"): HTMLButtonElement {
  return button(label, { type: "submit", variant });
}

export function list(...items: Child[]): HTMLElement {
  return h("ul", { class: "list" }, ...items);
}

export function row(...children: Child[]): HTMLElement {
  return h("li", { class: "row" }, ...children);
}

export function table(headers: string[], rows: Child[][]): HTMLElement {
  return h("div", { class: "table-wrap" }, h("table", {},
    h("thead", {}, h("tr", {}, ...headers.map((x) => h("th", {}, x)))),
    h("tbody", {}, ...rows.map((cells) => h("tr", {}, ...cells.map((c) => h("td", {}, c)))))));
}
