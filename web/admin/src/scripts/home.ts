/**
 * Home: the "finish setting up" checklist and one tile per section. Each tile summarises its
 * section's state in a few words; tiles for features report "needs setup" instead of raw errors.
 */
import { icon } from "../components/icons";
import { tile, tileGroup, type Status } from "../components/tile";
import { button, card, pageHeader } from "../components/ui";
import { t } from "../i18n";
import { api, type Status as DeviceStatus } from "./api";
import { formatUptime, h, when } from "./dom";
import { featureStatus, type Feature } from "./featureState";
import { GROUPS, SECTIONS, type Section } from "./sections";
import type { AuditRow } from "./views/dashboard";

interface Data {
  status: DeviceStatus | null;
  features: Feature[];
  telegram: { configured: boolean; enabled: boolean; running: boolean; bot_username: string; poll_error?: string } | null;
  users: unknown[] | null;
  chats: unknown[] | null;
  buttons: unknown[] | null;
  admins: unknown[] | null;
  assistant: { llm_key_configured: boolean } | null;
  wifi: { status: { available: boolean; connected: boolean; ssid?: string }; networks: unknown[] } | null;
  backup: { settings: { last_at?: string; last_error?: string } } | null;
  audit: AuditRow[] | null;
  system: { device_name: string } | null;
  spotify: { binary_found: boolean; signed_in: boolean; playing: boolean; track?: string; device_name: string } | null;
}

const soft = <T>(path: string): Promise<T | null> => api<T>("GET", path).catch(() => null);

async function load(): Promise<Data> {
  const [status, features, telegram, users, chats, buttons, admins, assistant, wifi, backup, audit, system, spotify] = await Promise.all([
    soft<DeviceStatus>("/api/status"), soft<Feature[]>("/api/features"), soft<Data["telegram"]>("/api/telegram"), soft<unknown[]>("/api/users"),
    soft<unknown[]>("/api/chats"), soft<unknown[]>("/api/buttons"), soft<unknown[]>("/api/admins"), soft<Data["assistant"]>("/api/assistant"),
    soft<Data["wifi"]>("/api/wifi"), soft<Data["backup"]>("/api/backup"), soft<AuditRow[]>("/api/audit"), soft<Data["system"]>("/api/system"), soft<Data["spotify"]>("/api/spotify"),
  ]);
  return { status, features: features ?? [], telegram, users, chats, buttons, admins, assistant, wifi, backup, audit, system, spotify };
}

interface Info {
  summary: string;
  status?: Status;
  hint?: string;
}

/** "none / one / n items" from the keys tile.<base>.none, .one and .other. */
const counted = (base: string, n: number): string => t(`tile.${base}.${n === 0 ? "none" : n === 1 ? "one" : "other"}`, { n });

function infoFor(s: Section, d: Data): Info {
  const feature = (name: string) => d.features.find((f) => f.name === name);
  const fs = (name: string) => featureStatus(feature(name));
  switch (s.id) {
    case "telegram": {
      if (!d.telegram) return { summary: t("tile.telegram.off") };
      if (!d.telegram.configured) return { summary: t("needs.telegramToken"), status: "setup" };
      if (!d.telegram.enabled) return { summary: t("tile.off"), status: "off" };
      if (d.telegram.poll_error) return { summary: t("tile.telegram.attention"), status: "attention", hint: d.telegram.poll_error };
      return d.telegram.running ? { summary: d.telegram.bot_username ? `@${d.telegram.bot_username}` : t("tile.telegram.ready"), status: "ready" } : { summary: t("tile.off"), status: "off" };
    }
    case "assistant": {
      const f = feature("assistant");
      if (d.assistant && !d.assistant.llm_key_configured) return { summary: t("needs.assistantKey"), status: "setup" };
      const st = fs("assistant");
      if (st.status === "ready") return { summary: t("tile.assistant.ready"), status: "ready" };
      if (st.status === "setup") return { summary: t(st.needsKey ?? "needs.assistantKey"), status: "setup", hint: f?.last_error };
      return st.status === "attention" ? { summary: t("tile.attention"), status: "attention", hint: f?.last_error } : { summary: t("tile.off"), status: "off" };
    }
    case "wakeword": {
      const f = feature("wakeword");
      const st = fs("wakeword");
      if (st.status === "ready") return { summary: t("tile.wakeword.ready"), status: "ready" };
      if (st.status === "setup") return { summary: t(st.needsKey ?? "needs.wakeSamples"), status: "setup", hint: f?.last_error };
      return st.status === "attention" ? { summary: t("tile.attention"), status: "attention", hint: f?.last_error } : { summary: t("tile.off"), status: "off" };
    }
    case "skills": {
      const list = (s.features ?? []).map(feature).filter((f): f is Feature => !!f);
      const on = list.filter((f) => f.enabled);
      const bad = on.find((f) => f.last_error && !featureStatus(f).needsKey);
      const summary = t("tile.skills.count", { on: on.length, total: list.length });
      return bad ? { summary, status: "attention", hint: bad.last_error } : { summary, status: on.length ? "ready" : "off" };
    }
    case "spotify": {
      const f = feature("spotify");
      const st = fs("spotify");
      if (st.status === "setup") return { summary: t(st.needsKey ?? "needs.spotifyBinary"), status: "setup", hint: f?.last_error };
      if (st.status === "attention") return { summary: t("tile.attention"), status: "attention", hint: f?.last_error };
      if (st.status !== "ready") return { summary: t("tile.off"), status: "off" };
      const sp = d.spotify;
      if (!sp?.signed_in) return { summary: t("tile.spotify.pair"), status: "setup" };
      return { summary: sp.playing && sp.track ? sp.track : t("tile.spotify.ready", { name: sp.device_name }), status: "ready" };
    }
    case "people": return { summary: counted("people", d.users?.length ?? 0) };
    case "chats": return { summary: counted("chats", d.chats?.length ?? 0) };
    case "buttons": return { summary: counted("buttons", d.buttons?.length ?? 0) };
    case "admins": return { summary: counted("admins", d.admins?.length ?? 0) };
    case "schedules": return { summary: t("tile.schedules") };
    case "wifi": {
      const w = d.wifi?.status;
      if (!w || !w.available) return { summary: t("tile.wifi.unavailable"), status: "off" };
      return w.connected ? { summary: t("wifi.connectedTo", { ssid: w.ssid ?? "" }), status: "ready" } : { summary: t("wifi.notConnected"), status: "attention" };
    }
    case "sound": {
      const a = d.status?.audio;
      return a?.available ? { summary: t("tile.sound.volume", { volume: a.volume }), status: "ready" } : { summary: t("dashboard.audioOff"), status: "attention", hint: a?.detail };
    }
    case "system": return { summary: d.system?.device_name ?? t("tile.system") };
    case "backup": {
      const b = d.backup?.settings;
      if (b?.last_error) return { summary: t("tile.backup.failed"), status: "attention", hint: b.last_error };
      return { summary: b?.last_at ? t("tile.backup.last", { when: when(b.last_at) }) : t("tile.backup.none") };
    }
    case "device": {
      const st = d.status;
      if (!st) return { summary: "" };
      return { summary: t("tile.device.summary", { version: st.version, uptime: formatUptime(st.uptime_seconds) }), status: st.hardware_errors?.length ? "attention" : undefined, hint: st.hardware_errors?.join("; ") };
    }
    case "activity": {
      const last = d.audit?.[0];
      return { summary: last ? t("tile.activity.last", { when: when(last.at) }) : t("tile.activity.none") };
    }
    case "simulator": return { summary: t("tile.simulator") };
    default: return { summary: "" };
  }
}

interface Step {
  id: string;
  section: string;
  done: boolean;
  optional?: boolean;
}

function steps(d: Data): Step[] {
  const out: Step[] = [];
  if (d.telegram) out.push({ id: "telegram", section: "telegram", done: d.telegram.configured });
  if (d.users) out.push({ id: "people", section: "telegram", done: d.users.length > 0 });
  if (d.assistant) out.push({ id: "assistant", section: "assistant", done: d.assistant.llm_key_configured, optional: true });
  if (d.wifi?.status.available) out.push({ id: "wifi", section: "wifi", done: d.wifi.status.connected || d.wifi.networks.length > 0, optional: true });
  return out;
}

/** The checklist, shown only while a required step is still open. */
function setupCard(list: Step[], open: (id: string) => void): HTMLElement | null {
  if (!list.some((s) => !s.done && !s.optional)) return null;
  const done = list.filter((s) => s.done).length;
  return card(t("home.setup.title"),
    h("p", { class: "muted" }, t("home.setup.progress", { done, total: list.length })),
    h("ol", { class: "steps" }, ...list.map((s) => h("li", { class: s.done ? "step done" : "step" },
      h("span", { class: "step-mark", "aria-hidden": "true" }, s.done ? icon("check", 16) : null),
      h("span", { class: "step-text" },
        h("strong", {}, t(`home.step.${s.id}`), s.optional ? h("span", { class: "pill off" }, t("home.setup.optional")) : null),
        h("span", { class: "muted" }, t(`home.step.${s.id}.hint`))),
      s.done ? h("span", { class: "sr-only" }, t("home.setup.done")) : button(t("home.setup.open"), { variant: "secondary", onclick: () => open(s.section) })))));
}

/** Renders Home into `root` (replacing what was there once the data has arrived). */
export async function renderHome(root: HTMLElement, open: (id: string) => void, isDev: boolean): Promise<void> {
  const d = await load();
  const page = h("div", { class: "stack home" }, pageHeader(t("home.title"), t("home.subtitle")), setupCard(steps(d), open));
  for (const g of GROUPS) {
    const tiles = SECTIONS.filter((s) => s.group === g && (!s.devOnly || isDev)).map((s) => {
      const info = infoFor(s, d);
      return tile({ id: s.id, title: t(`nav.${s.id}`), summary: info.summary, icon: s.icon, tone: s.tone, status: info.status, hint: info.hint, onopen: () => open(s.id) });
    });
    if (tiles.length) page.append(tileGroup(t(`home.group.${g}`), ...tiles));
  }
  root.replaceChildren(page);
}
