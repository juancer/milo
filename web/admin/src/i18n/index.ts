/**
 * Translation layer: key -> current locale -> English -> the key itself.
 * A missing locale or key never throws. Add a language by dropping a
 * `locales/<code>.json` file next to en.json; nothing else needs to change.
 */
type Catalog = Record<string, string>;

const modules = import.meta.glob<Catalog>("./locales/*.json", { eager: true, import: "default" });
const catalogs: Record<string, Catalog> = {};
for (const [path, catalog] of Object.entries(modules)) {
  const code = /\/([^/]+)\.json$/.exec(path)?.[1];
  if (code) catalogs[code] = catalog;
}

export const DEFAULT_LOCALE = "en";
const STORAGE_KEY = "milo.locale";
let current = DEFAULT_LOCALE;

/** "es-ES" -> "es"; anything unknown -> English. */
export function normalizeLocale(code: string | null | undefined): string {
  const base = (code ?? "").toLowerCase().split(/[-_]/)[0];
  return base in catalogs ? base : DEFAULT_LOCALE;
}

export function getLocale(): string {
  return current;
}

export function setLocale(code: string | null | undefined): void {
  current = normalizeLocale(code);
  if (typeof document !== "undefined") document.documentElement.lang = current;
  try {
    localStorage.setItem(STORAGE_KEY, current);
  } catch {
    /* storage may be blocked */
  }
}

/** The locale used last time in this browser, so the sign-in page speaks it too. */
export function storedLocale(): string | null {
  try {
    return localStorage.getItem(STORAGE_KEY);
  } catch {
    return null;
  }
}

function lookup(key: string): string | undefined {
  return catalogs[current]?.[key] ?? catalogs[DEFAULT_LOCALE]?.[key];
}

function fill(text: string, vars?: Record<string, string | number>): string {
  return vars ? text.replace(/\{(\w+)\}/g, (m, name: string) => (name in vars ? String(vars[name]) : m)) : text;
}

/** Translates `key`; returns the key itself if no catalog has it. */
export function t(key: string, vars?: Record<string, string | number>): string {
  return fill(lookup(key) ?? key, vars);
}

/** Like t(), but returns `fallback` for a key nobody translated (e.g. server-defined names). */
export function tOr(key: string, fallback: string, vars?: Record<string, string | number>): string {
  const found = lookup(key);
  return found === undefined ? fallback : fill(found, vars);
}

/** Languages shipped with the UI, each named in its own language. */
export function localeList(): { code: string; name: string }[] {
  return Object.keys(catalogs)
    .sort()
    .map((code) => ({ code, name: catalogs[code]["meta.name"] ?? code }));
}

/** Re-translates static markup: data-i18n (text) and data-i18n-<attr> (placeholder, aria-label, title). */
export function applyI18n(root: ParentNode = document): void {
  for (const el of root.querySelectorAll<HTMLElement>("[data-i18n]")) el.textContent = t(el.dataset.i18n ?? "");
  for (const el of root.querySelectorAll<HTMLElement>("*")) {
    for (const attr of ["placeholder", "aria-label", "title"]) {
      const key = el.getAttribute(`data-i18n-${attr}`);
      if (key) el.setAttribute(attr, t(key));
    }
  }
}
