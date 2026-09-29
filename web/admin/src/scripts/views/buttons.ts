import { api } from "../api";
import { clear, confirmTwice, errorMessage, field, h, toast } from "../dom";

interface ButtonAction {
  action: string;
  parameters: Record<string, unknown>;
}
interface Button {
  id: number;
  name: string;
  color: string;
  gpio: number;
  active_low: boolean;
  enabled: boolean;
  actions: Record<string, ButtonAction>;
  unknown_actions?: string[];
}
interface ActionInfo {
  name: string;
  description: string;
  feature?: string;
}

const EVENTS: [string, string][] = [
  ["short_press", "Short press"],
  ["long_press", "Long press"],
  ["double_press", "Double press"],
];

export async function render(root: HTMLElement): Promise<void> {
  const [buttons, actions] = await Promise.all([
    api<Button[]>("GET", "/api/buttons"),
    api<ActionInfo[]>("GET", "/api/actions"),
  ]);
  clear(root);
  root.append(
    h("p", { class: "muted" },
      "Each button is a physical input on a GPIO pin (BCM numbering). What it does is only this mapping - change it here, no code needed."),
    ...buttons.map((b) => card(root, b, actions)),
    card(root, { id: 0, name: "", color: "", gpio: 4, active_low: true, enabled: true, actions: {} }, actions),
  );
}

function card(root: HTMLElement, b: Button, actions: ActionInfo[]): HTMLElement {
  const isNew = b.id === 0;
  const name = h("input", { type: "text", value: b.name, maxLength: 40, required: true, ariaLabel: "Name" });
  const color = h("input", { type: "text", value: b.color, maxLength: 20, ariaLabel: "Color label" });
  const gpio = h("input", { type: "number", value: String(b.gpio), min: "0", max: "27", ariaLabel: "GPIO pin" });
  const enabled = h("input", { type: "checkbox", checked: b.enabled });
  const activeLow = h("input", { type: "checkbox", checked: b.active_low });

  const known = new Set(actions.map((a) => a.name));
  const rows = EVENTS.map(([event, label]) => {
    const current = b.actions[event];
    const select = h("select", { ariaLabel: `${label} action` },
      h("option", { value: "" }, "(nothing)"),
      ...actions.map((a) => h("option", { value: a.name, selected: current?.action === a.name, title: a.description }, a.name)),
      current && !known.has(current.action)
        ? h("option", { value: current.action, selected: true }, `${current.action} (not available yet)`)
        : null,
    );
    const params = h("input", {
      type: "text", ariaLabel: `${label} parameters (JSON)`, placeholder: '{"change": 10}',
      value: current && Object.keys(current.parameters).length ? JSON.stringify(current.parameters) : "",
    });
    return { event, select, params, row: h("div", { class: "map" }, h("span", {}, label), select, params) };
  });

  const form = h("form", {
    class: "card",
    onsubmit: async (ev: Event) => {
      ev.preventDefault();
      const payload: Record<string, unknown> = {};
      for (const r of rows) {
        if (!r.select.value) continue;
        try {
          payload[r.event] = { action: r.select.value, parameters: r.params.value.trim() ? JSON.parse(r.params.value) : {} };
        } catch {
          toast(`${r.event}: parameters must be valid JSON`, true);
          return;
        }
      }
      try {
        await api(isNew ? "POST" : "PUT", isNew ? "/api/buttons" : `/api/buttons/${b.id}`, {
          name: name.value.trim(), color: color.value.trim(), gpio: Number(gpio.value),
          active_low: activeLow.checked, enabled: enabled.checked, actions: payload,
        });
        toast("Saved");
        await render(root);
      } catch (e) {
        toast(errorMessage(e), true);
      }
    },
  },
    h("h2", {}, isNew ? "Add a button" : b.name),
    h("div", { class: "fields" },
      field("Name", name), field("Color label", color), field("GPIO pin", gpio),
      h("label", { class: "check" }, enabled, " Enabled"),
      h("label", { class: "check", title: "Wired to ground with the internal pull-up (the usual way)" }, activeLow, " Active low"),
    ),
    ...rows.map((r) => r.row),
    h("div", { class: "actions" },
      h("button", { type: "submit" }, isNew ? "Add" : "Save"),
      isNew ? null : h("button", {
        type: "button", class: "danger",
        onclick: async (ev: Event) => {
          if (!confirmTwice(ev.currentTarget as HTMLButtonElement, "Delete")) return;
          try {
            await api("DELETE", `/api/buttons/${b.id}`);
            await render(root);
          } catch (e) {
            toast(errorMessage(e), true);
          }
        },
      }, "Delete"),
    ),
  );
  return form;
}
