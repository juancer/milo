import { api } from "./api";
import { localeList } from "../i18n";

export interface LocaleOption {
  code: string;
  name: string;
}

/** Sentinel for "detect the language from what the user says" (assistant language only). */
export const AUTO = "auto";

let cached: LocaleOption[] | undefined;

/**
 * Languages the device offers, from GET /api/locales (a list of codes or of
 * {code, name}). If the endpoint is missing, the languages bundled in the UI are used.
 */
export async function loadLocales(): Promise<LocaleOption[]> {
  if (cached) return cached;
  const bundled = localeList();
  const nameOf = (code: string) => bundled.find((l) => l.code === code)?.name ?? code;
  try {
    const raw = await api<unknown>("GET", "/api/locales");
    const list = Array.isArray(raw) ? raw : (raw as { locales?: unknown[] }).locales;
    if (Array.isArray(list) && list.length > 0) {
      cached = list.map((item) => {
        const o = typeof item === "string" ? { code: item, name: "" } : (item as { code: string; name?: string });
        return { code: o.code, name: o.name || nameOf(o.code) };
      });
      return cached;
    }
  } catch {
    /* older device: fall through */
  }
  cached = bundled;
  return cached;
}
