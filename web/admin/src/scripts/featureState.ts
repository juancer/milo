/**
 * Feature on/off with friendly states. A feature that cannot start because something is not set up
 * yet reports a technical error; here it becomes a plain "needs setup" message, with the raw text
 * kept for a tooltip / details only.
 */
import { pill, switchInput, type Status } from "../components/tile";
import { button, row } from "../components/ui";
import { t, tOr } from "../i18n";
import { api } from "./api";
import { h } from "./dom";
import { enableFeature } from "./setupPrompt";
import { guard } from "./ui";

export interface Feature {
  name: string;
  enabled: boolean;
  running: boolean;
  last_error?: string;
}

const KNOWN_ERRORS: [RegExp, string][] = [
  [/bot token/i, "needs.telegramToken"],
  [/API key/i, "needs.assistantKey"],
  [/wake-word samples/i, "needs.wakeSamples"],
  [/no place is set/i, "needs.weatherPlace"],
];

/** Translation key of the friendly explanation for a known "not set up yet" error, if it is one. */
export function needsSetupKey(error: string | undefined): string | null {
  if (!error) return null;
  return KNOWN_ERRORS.find(([re]) => re.test(error))?.[1] ?? null;
}

/** Status of one feature: off, needs setup, attention (unexpected error), or ready. */
export function featureStatus(f: Feature | undefined): { status: Status; needsKey: string | null } {
  if (!f || !f.enabled) return { status: "off", needsKey: null };
  const needsKey = needsSetupKey(f.last_error);
  if (needsKey) return { status: "setup", needsKey };
  if (f.last_error) return { status: "attention", needsKey: null };
  return { status: f.running ? "ready" : "off", needsKey: null };
}

/**
 * The switches shown at the top of a section's sheet. `onchange` runs after the server answered so
 * the caller can refresh the view underneath.
 */
export function featureStrip(features: Feature[], onchange: () => void): HTMLElement | null {
  if (features.length === 0) return null;
  return h("section", { class: "feature-strip" }, h("ul", { class: "list" }, ...features.map((f) => {
    const { status, needsKey } = featureStatus(f);
    const sw = switchInput(f.enabled, t("dashboard.enable", { name: tOr(`feature.${f.name}`, f.name) }), {
      onchange: async () => {
        sw.disabled = true;
        const on = sw.checked;
        // Turning on may ask for what is missing first; cancelling leaves the feature off, silently.
        const ok = await guard(async () => {
          if (on) sw.checked = await enableFeature(f.name);
          else await api("PUT", `/api/features/${encodeURIComponent(f.name)}`, { enabled: false });
        });
        if (!ok) sw.checked = !on;
        sw.disabled = false;
        onchange();
      },
    });
    const state = needsKey ? t(needsKey) : f.last_error ? t("feature.failed") : f.enabled && f.running ? "" : t("dashboard.stopped");
    return row(
      h("span", { class: "feature-name" }, tOr(`feature.${f.name}`, f.name), " ", f.enabled ? pill(status, f.last_error) : null),
      h("span", { class: "muted feature-state" }, state),
      needsKey ? button(t("home.setup.open"), { variant: "secondary", onclick: () => setUp(f, needsKey, onchange) }) : null,
      f.last_error && !needsKey ? h("details", {}, h("summary", {}, t("feature.details")), h("p", { class: "muted" }, f.last_error)) : null,
      sw);
  })));
}

/** "Set up" on an enabled-but-unconfigured feature: ask for the missing keys, or point at the form below. */
async function setUp(f: Feature, needsKey: string, onchange: () => void): Promise<void> {
  if (needsKey === "needs.wakeSamples") {
    document.querySelector(".feature-strip")?.nextElementSibling?.scrollIntoView({ behavior: "smooth" });
    return;
  }
  if (await guard(() => enableFeature(f.name))) onchange();
}
