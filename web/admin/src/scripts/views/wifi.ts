import { api } from "../api";
import { badge, clear, confirmTwice, errorMessage, field, h, toast, when } from "../dom";

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
  root.append(h("section", { class: "card" }, h("h2", {}, "Wi-Fi"),
    h("p", {}, st.connected ? badge(`connected to ${st.ssid}`) : badge("not connected", true), " ", st.detail ? h("span", { class: "muted" }, st.detail) : null),
    h("p", { class: "muted" },
      "The device joins the highest-priority network it can see. Add your home, your grandparents' and a phone hotspot. Passwords are stored encrypted. You need Internet access on the Pi for the first setup.")));
  for (const n of state.networks) root.append(card(n, refresh));
  root.append(card({ id: 0, ssid: "", priority: 5, enabled: true, open: false }, refresh));
}

function card(n: Network, refresh: () => Promise<void>): HTMLElement {
  const isNew = n.id === 0;
  const ssid = h("input", { type: "text", value: n.ssid, maxLength: 32, required: true, ariaLabel: "Network name" });
  const pass = h("input", { type: "password", autocomplete: "new-password", maxLength: 63, ariaLabel: "Password", placeholder: isNew ? "" : n.open ? "(open network)" : "(saved - type to replace)" });
  const priority = h("input", { type: "number", value: String(n.priority), min: "0", max: "100", ariaLabel: "Priority" });
  const enabled = h("input", { type: "checkbox", checked: n.enabled });
  const open = h("input", { type: "checkbox", checked: n.open });
  return h("form", {
    class: "card",
    onsubmit: async (ev: Event) => {
      ev.preventDefault();
      try {
        const res = await api<{ warning?: string }>(isNew ? "POST" : "PUT", isNew ? "/api/wifi" : `/api/wifi/${n.id}`, {
          ssid: ssid.value, password: pass.value, priority: Number(priority.value), enabled: enabled.checked, open: open.checked,
        });
        toast(res.warning ?? "Saved", Boolean(res.warning));
        await refresh();
      } catch (e) {
        toast(errorMessage(e), true);
      }
    },
  },
    h("h2", {}, isNew ? "Add a network" : n.ssid, " ", n.last_connected ? h("span", { class: "muted" }, `last connected ${when(n.last_connected)}`) : null),
    h("div", { class: "fields" }, field("Network name", ssid), field("Password", pass), field("Priority (higher first)", priority),
      h("label", { class: "check" }, open, " Open network (no password)"), h("label", { class: "check" }, enabled, " Enabled")),
    h("div", { class: "actions" }, h("button", { type: "submit" }, isNew ? "Add" : "Save"),
      isNew ? null : h("button", {
        type: "button", class: "danger",
        onclick: async (ev: Event) => {
          if (!confirmTwice(ev.currentTarget as HTMLButtonElement, "Remove")) return;
          await api("DELETE", `/api/wifi/${n.id}`);
          await refresh();
        },
      }, "Remove")));
}
