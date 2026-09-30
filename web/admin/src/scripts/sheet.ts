/**
 * The sheet: a modal panel (centred on desktop, a bottom sheet on phones) that hosts one section's
 * view. It owns focus (trap + return), scroll lock, Esc and backdrop dismissal. What "dismiss" does
 * (change the URL, stop the view) is decided by the shell through initSheet().
 */
import { icon } from "../components/icons";
import { iconBadge, type Tone } from "../components/tile";
import type { IconName } from "../components/icons";
import { clear } from "./dom";

const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), summary, [tabindex]:not([tabindex="-1"])';

let backdrop: HTMLElement;
let panel: HTMLElement;
let body: HTMLElement;
let returnTo: string | null = null;

const confirmOpen = () => document.getElementById("confirm-dialog")?.hidden === false;
const shell = () => document.querySelectorAll<HTMLElement>("header.bar, main");

export function initSheet(dismiss: () => void): void {
  backdrop = document.getElementById("sheet") as HTMLElement;
  panel = backdrop.querySelector(".modal") as HTMLElement;
  body = document.getElementById("sheet-body") as HTMLElement;
  const close = document.getElementById("sheet-close") as HTMLElement;
  close.append(icon("close", 20));
  close.addEventListener("click", dismiss);
  backdrop.addEventListener("mousedown", (ev) => {
    if (ev.target === backdrop) dismiss();
  });
  document.addEventListener("keydown", (ev) => {
    if (backdrop.hidden || confirmOpen()) return;
    if (ev.key === "Escape") return dismiss();
    if (ev.key !== "Tab") return;
    const items = [...panel.querySelectorAll<HTMLElement>(FOCUSABLE)].filter((el) => el.offsetParent !== null);
    if (items.length === 0) return ev.preventDefault();
    const first = items[0];
    const last = items[items.length - 1];
    const now = document.activeElement;
    if (ev.shiftKey && (now === first || now === panel)) (ev.preventDefault(), last.focus());
    else if (!ev.shiftKey && now === last) (ev.preventDefault(), first.focus());
  });
}

export const isSheetOpen = (): boolean => !backdrop.hidden;

/** Opens the sheet (or re-titles it when already open) and returns the element to render into. */
export function showSheet(title: string, glyph: IconName, tone: Tone, opener: string): HTMLElement {
  const fresh = backdrop.hidden;
  if (fresh) {
    returnTo = opener;
    backdrop.hidden = false;
    document.body.classList.add("sheet-open");
    shell().forEach((el) => el.setAttribute("inert", ""));
  }
  (document.getElementById("sheet-title") as HTMLElement).textContent = title;
  const badge = document.getElementById("sheet-icon") as HTMLElement;
  clear(badge);
  badge.append(iconBadge(glyph, tone, 20));
  clear(body);
  body.scrollTop = 0;
  if (fresh) panel.focus();
  return body;
}

/** Replaces the sheet's content in one step (used once the view has finished rendering). */
export function fillSheet(content: HTMLElement): void {
  body.replaceChildren(content);
}

export function hideSheet(): void {
  if (backdrop.hidden) return;
  backdrop.hidden = true;
  document.body.classList.remove("sheet-open");
  shell().forEach((el) => el.removeAttribute("inert"));
  clear(body);
  const opener = returnTo ? document.querySelector<HTMLElement>(`[data-tile="${returnTo}"]`) : null;
  returnTo = null;
  opener?.focus();
}

