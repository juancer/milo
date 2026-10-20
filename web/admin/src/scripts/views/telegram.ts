import { actions, badge, button, card, checkbox, emptyState, field, fields, formCard, list, note, row, select, submit, table, toggle } from "../../components/ui";
import { t, tOr } from "../../i18n";
import { api } from "../api";
import { clear, h, when } from "../dom";
import { confirmTwice, guard } from "../ui";

interface TelegramStatus {
  enabled: boolean;
  configured: boolean;
  running: boolean;
  bot_username: string;
  poll_error?: string;
  pending_messages: number;
  urgent_messages: number;
}
interface Invitation {
  id: number;
  role: string;
  note: string;
  status: string;
  expires_at: string;
}
interface Request {
  id: number;
  telegram_id: number;
  username: string;
  first_name: string;
  role: string;
  created_at: string;
}
interface Message {
  id: number;
  direction: string;
  kind: string;
  type: string;
  status: string;
  person: string;
  created_at: string;
  attempts: number;
  last_error?: string;
}

let lastInvite: { link: string; command: string; expires_at: string } | null = null;

export async function render(root: HTMLElement): Promise<void> {
  const [st, invites, requests, messages, alertsCfg] = await Promise.all([
    api<TelegramStatus>("GET", "/api/telegram"),
    api<Invitation[]>("GET", "/api/invitations"),
    api<Request[]>("GET", "/api/authorizations"),
    api<Message[]>("GET", "/api/messages"),
    api<{ enabled: boolean; level: string; detail: string }>("GET", "/api/telegram/error-alerts"),
  ]);
  clear(root);
  const refresh = () => render(root);

  // ---- bot status and token
  const token = h("input", { type: "password", autocomplete: "off", placeholder: "123456:ABC-DEF…", ariaLabel: t("telegram.botToken") });
  const state = !st.enabled ? badge(t("common.featureOff"), true) : st.running ? badge(t("common.running")) : badge(t("common.notRunning"), true);
  root.append(card(t("telegram.bot"),
    h("p", {}, state, " ", st.bot_username ? h("strong", {}, `@${st.bot_username}`) : null,
      st.enabled ? null : h("span", { class: "muted" }, ` ${t("telegram.turnOn")}`)),
    st.poll_error ? h("p", { class: "error" }, t("telegram.connectionProblem", { error: st.poll_error })) : null,
    note(t("telegram.tokenHelp")),
    h("form", {
      class: "inline",
      onsubmit: async (ev: Event) => {
        ev.preventDefault();
        if (await guard(() => api("PUT", "/api/telegram/token", { token: token.value.trim() }), "telegram.tokenSaved")) {
          token.value = "";
          await refresh();
        }
      },
    }, token, h("button", { type: "submit" }, st.configured ? t("telegram.replaceToken") : t("telegram.saveToken")),
      st.configured ? button(t("telegram.removeToken"), {
        variant: "danger",
        onclick: async (ev) => {
          if (!confirmTwice(ev.currentTarget as HTMLButtonElement)) return;
          if (await guard(() => api("DELETE", "/api/telegram/token"))) await refresh();
        },
      }) : null),
    note(st.urgent_messages
      ? t("telegram.pendingUrgent", { count: st.pending_messages, urgent: st.urgent_messages })
      : t("telegram.pending", { count: st.pending_messages })),
  ));

  // ---- errors sent to Telegram (the owner decides how much of the log leaves the device)
  const alertsOn = checkbox(alertsCfg.enabled);
  const alertsLevel = select(t("telegram.errorAlertsLevel"), [["error", t("telegram.errorAlertsLevelError")], ["warn", t("telegram.errorAlertsLevelWarn")]], alertsCfg.level);
  const alertsDetail = select(t("telegram.errorAlertsDetail"), [
    ["none", t("telegram.errorAlertsDetailNone")], ["safe", t("telegram.errorAlertsDetailSafe")], ["full", t("telegram.errorAlertsDetailFull")],
  ], alertsCfg.detail);
  root.append(formCard(async (ev) => {
    ev.preventDefault();
    await guard(() => api("PUT", "/api/telegram/error-alerts", { enabled: alertsOn.checked, level: alertsLevel.value, detail: alertsDetail.value }), "common.saved");
    await refresh();
  },
    h("h2", {}, t("telegram.errorAlerts")),
    note(t("telegram.errorAlertsHelp")),
    h("label", { class: "field" }, alertsOn, " ", t("telegram.errorAlertsOn")),
    fields(field(t("telegram.errorAlertsLevel"), alertsLevel), field(t("telegram.errorAlertsDetail"), alertsDetail)),
    note(t("telegram.errorAlertsFullWarning")),
    actions(submit(t("common.save")))));

  // ---- requests waiting for approval
  root.append(card(t("telegram.requests"),
    requests.length === 0 ? emptyState(t("telegram.noRequests")) : null,
    ...requests.map((r) => requestRow(r, refresh))));

  // ---- invitations
  const role = select(t("common.role"), ["PARENT", "FAMILY", "CHILD"].map((r) => [r, t(`role.${r}`)]), "FAMILY");
  const hours = h("input", { type: "number", value: "24", min: "1", max: "168", ariaLabel: t("telegram.validHours") });
  const noteInput = h("input", { type: "text", maxLength: 60, placeholder: t("telegram.noteExample"), ariaLabel: t("telegram.note") });
  root.append(card(t("telegram.invite"),
    note(t("telegram.inviteHelp")),
    h("form", {
      onsubmit: async (ev: Event) => {
        ev.preventDefault();
        if (await guard(async () => {
          lastInvite = await api<{ link: string; command: string; expires_at: string }>("POST", "/api/invitations", {
            role: role.value, ttl_hours: Number(hours.value), note: noteInput.value.trim(),
          });
        })) await refresh();
      },
    }, fields(field(t("common.role"), role), field(t("telegram.validFor"), hours), field(t("telegram.note"), noteInput), submit(t("telegram.createInvitation")))),
    lastInvite ? h("div", { class: "card" },
      h("strong", {}, t("telegram.sendNow")),
      h("input", { type: "text", readOnly: true, value: lastInvite.link || lastInvite.command, ariaLabel: t("telegram.inviteLink"), onfocus: (e: Event) => (e.target as HTMLInputElement).select() }),
      lastInvite.link ? null : note(t("telegram.noBotUsername")),
      note(t("telegram.expires", { when: when(lastInvite.expires_at) }))) : null,
    table([t("common.role"), t("telegram.note"), t("common.status"), t("telegram.expiresCol"), ""], invites.map((i) => [
      t(`role.${i.role}`), i.note, badge(tOr(`status.${i.status}`, i.status), i.status === "revoked"), when(i.expires_at),
      i.status === "active" ? button(t("telegram.revoke"), {
        variant: "secondary",
        onclick: async () => { if (await guard(() => api("DELETE", `/api/invitations/${i.id}`))) await refresh(); },
      }) : null,
    ]))));

  // ---- message flow (no content)
  root.append(card(t("telegram.activity"),
    note(t("telegram.activityHelp")),
    messages.length === 0 ? emptyState(t("telegram.noMessages")) : null,
    list(...messages.map((m) => row(
      h("span", {}, m.direction === "in" ? t("telegram.from") : t("telegram.to"), " ", m.person || "?", " · ", tOr(`kind.${m.kind}`, m.kind), m.type === "emergency" ? badge(t("telegram.emergency"), true) : null),
      h("span", { class: m.status === "failed" ? "error" : "muted" }, `${tOr(`status.${m.status}`, m.status)}${m.last_error ? ` (${m.last_error})` : ""} · ${when(m.created_at)}`))))));
}

function requestRow(r: Request, refresh: () => Promise<void>): HTMLElement {
  const name = h("input", { type: "text", value: r.first_name, maxLength: 40, ariaLabel: t("telegram.displayName") });
  const chat = checkbox(r.role !== "CHILD");
  return h("div", { class: "card" },
    h("strong", {}, t("telegram.newRequest")),
    h("p", {}, t("telegram.requestInfo", {
      name: r.first_name || t("telegram.noName"), username: r.username ? `@${r.username}` : t("telegram.noUsername"), id: r.telegram_id, role: t(`role.${r.role}`),
    })),
    fields(field(t("telegram.showAs"), name), toggle(t("telegram.addAsChat"), chat)),
    actions(
      button(t("telegram.approve"), {
        onclick: async () => {
          if (await guard(() => api("POST", `/api/authorizations/${r.id}/approve`, { display_name: name.value.trim(), create_chat: chat.checked }), "telegram.approved")) await refresh();
        },
      }),
      button(t("telegram.reject"), { variant: "danger", onclick: async () => { if (await guard(() => api("POST", `/api/authorizations/${r.id}/reject`))) await refresh(); } })));
}
