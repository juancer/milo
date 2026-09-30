import { actions, badge, button, card, checkbox, field, fields, formCard, note, submit, toggle } from "../../components/ui";
import { t } from "../../i18n";
import { api } from "../api";
import { clear, errorMessage, h, when } from "../dom";
import { confirmTwice, guard, toast } from "../ui";

interface Network {
  id: number;
  ssid: string;
  priority: number;
  enabled: boolean;
  open: boolean;
  last_connected?: string;
}
interface State {
  status: { available: boolean; connected: boolean; ssid?: string; detail?: string };
  networks: Network[];
}

export async function render(root: HTMLElement): Promise<void> {
  const state = await api<State>("GET", "/api/wifi");
  clear(root);
  const refresh = () => render(root);
  const st = state.status;
  root.append(card(t("wifi.title"),
    h("p", {}, st.connected ? badge(t("wifi.connectedTo", { ssid: st.ssid ?? "" })) : badge(t("wifi.notConnected"), true), " ", st.detail ? h("span", { class: "muted" }, st.detail) : null),
    note(t("wifi.help"))));
  for (const n of state.networks) root.append(networkCard(n, refresh));
  root.append(networkCard({ id: 0, ssid: "", priority: 5, enabled: true, open: false }, refresh));
}

function networkCard(n: Network, refresh: () => Promise<void>): HTMLElement {
  const isNew = n.id === 0;
  const ssid = h("input", { type: "text", value: n.ssid, maxLength: 32, required: true, ariaLabel: t("wifi.name") });
  const pass = h("input", { type: "password", autocomplete: "new-password", maxLength: 63, ariaLabel: t("wifi.password"), placeholder: isNew ? "" : n.open ? t("wifi.openNetwork") : t("wifi.passwordSaved") });
  const priority = h("input", { type: "number", value: String(n.priority), min: "0", max: "100", ariaLabel: t("wifi.priority") });
  const enabled = checkbox(n.enabled);
  const open = checkbox(n.open);
  return formCard(async (ev) => {
    ev.preventDefault();
    try {
      const res = await api<{ warning?: string }>(isNew ? "POST" : "PUT", isNew ? "/api/wifi" : `/api/wifi/${n.id}`, {
        ssid: ssid.value, password: pass.value, priority: Number(priority.value), enabled: enabled.checked, open: open.checked,
      });
      toast(res.warning ?? t("common.saved"), Boolean(res.warning));
      await refresh();
    } catch (e) {
      toast(errorMessage(e), true);
    }
  },
    h("h2", {}, isNew ? t("wifi.add") : n.ssid, " ", n.last_connected ? h("span", { class: "muted" }, t("wifi.lastConnected", { when: when(n.last_connected) })) : null),
    fields(field(t("wifi.name"), ssid), field(t("wifi.password"), pass), field(t("wifi.priorityLabel"), priority),
      toggle(t("wifi.open"), open), toggle(t("common.enabled"), enabled)),
    actions(submit(isNew ? t("common.add") : t("common.save")),
      isNew ? null : button(t("common.remove"), {
        variant: "danger",
        onclick: async (ev) => {
          if (!confirmTwice(ev.currentTarget as HTMLButtonElement)) return;
          if (await guard(() => api("DELETE", `/api/wifi/${n.id}`))) await refresh();
        },
      })));
}
