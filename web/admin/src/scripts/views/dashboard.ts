import { card, emptyState, list, row } from "../../components/ui";
import { tOr, t } from "../../i18n";
import { api, type Status } from "../api";
import { clear, formatUptime, h, when } from "../dom";

export interface AuditRow {
  at: string;
  actor: string;
  action: string;
  target: string;
}

/** Device facts: version, uptime, hardware, audio. */
export async function renderDevice(root: HTMLElement): Promise<void> {
  const status = await api<Status>("GET", "/api/status");
  clear(root);

  const facts: [string, string][] = [
    [t("dashboard.version"), status.version],
    [t("dashboard.uptime"), formatUptime(status.uptime_seconds)],
    [t("dashboard.hardware"), status.hardware],
    [t("dashboard.platform"), status.arch],
    [t("dashboard.audio"), status.audio.available
      ? t("dashboard.audioOk", { backend: status.audio.backend, volume: status.audio.volume })
      : t("dashboard.audioOff") + (status.audio.detail ? ` (${status.audio.detail})` : "")],
    [t("dashboard.schema"), String(status.schema_version)],
  ];
  root.append(card(t("dashboard.device"),
    h("dl", { class: "grid" }, ...facts.map(([k, v]) => h("div", {}, h("dt", {}, k), h("dd", {}, v)))),
    ...(status.hardware_errors ?? []).map((e) => h("p", { class: "error", role: "alert" }, e))));
}

/** The recent audit trail, with each action in plain words. */
export async function renderActivity(root: HTMLElement): Promise<void> {
  const audit = await api<AuditRow[]>("GET", "/api/audit");
  clear(root);
  root.append(card(t("dashboard.activity"),
    audit.length === 0 ? emptyState(t("dashboard.noActivity")) : null,
    list(...audit.map((r) => row(
      h("span", {}, r.target ? `${tOr(`audit.${r.action}`, r.action)} · ${r.target}` : tOr(`audit.${r.action}`, r.action)),
      h("span", { class: "muted" }, `${r.actor} · ${when(r.at)}`))))));
}
