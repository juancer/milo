/** Tile, status pill and switch builders for the Home screen and the section sheets. */
import { h, type Child } from "../scripts/dom";
import { t } from "../i18n";
import { icon, type IconName } from "./icons";

export type Tone = "blue" | "green" | "orange" | "red" | "purple" | "teal" | "pink" | "indigo" | "gray";
export type Status = "ready" | "setup" | "off" | "attention";

/** Coloured, translated state pill. `hint` becomes a tooltip (used for raw technical detail). */
export function pill(status: Status, hint?: string): HTMLElement {
  return h("span", { class: `pill ${status}`, title: hint }, t(`status.${status}`));
}

/** The rounded coloured square that carries a section's glyph. */
export function iconBadge(name: IconName, tone: Tone, size = 22): HTMLElement {
  return h("span", { class: `icon-badge tone-${tone}`, "aria-hidden": "true" }, icon(name, size));
}

interface TileProps {
  id: string;
  title: string;
  summary: string;
  icon: IconName;
  tone: Tone;
  status?: Status;
  hint?: string;
  onopen: () => void;
}

/** One big tappable tile: icon, title, one-line summary, status pill. */
export function tile(p: TileProps): HTMLElement {
  return h("button", { type: "button", class: "tile", "data-tile": p.id, ariaHasPopup: "dialog", onclick: p.onopen },
    h("span", { class: "tile-top" }, iconBadge(p.icon, p.tone), p.status ? pill(p.status, p.hint) : null),
    h("span", { class: "tile-title" }, p.title),
    h("span", { class: "tile-summary" }, p.summary));
}

/** A labelled group of tiles. */
export function tileGroup(title: string, ...tiles: Child[]): HTMLElement {
  return h("section", { class: "tile-group" }, h("h3", {}, title), h("div", { class: "tiles" }, ...tiles));
}

/** iOS-style switch; the input keeps its native semantics. */
export function switchInput(checked: boolean, label: string, props: Record<string, unknown> = {}): HTMLInputElement {
  return h("input", { type: "checkbox", class: "switch", role: "switch", checked, ariaLabel: label, ...props });
}
