import { actions, button, checkbox, field, fields, formCard, note, submit, toggle } from "../../components/ui";
import { t } from "../../i18n";
import { api } from "../api";
import { clear, h } from "../dom";
import { confirmTwice, guard, toast } from "../ui";

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

const EVENTS = ["short_press", "long_press", "double_press"];

export async function render(root: HTMLElement): Promise<void> {
  const [buttons, actionList] = await Promise.all([
    api<Button[]>("GET", "/api/buttons"),
    api<ActionInfo[]>("GET", "/api/actions"),
  ]);
  clear(root);
  root.append(
    note(t("buttons.intro")),
    ...buttons.map((b) => buttonCard(root, b, actionList)),
    buttonCard(root, { id: 0, name: "", color: "", gpio: 4, active_low: true, enabled: true, actions: {} }, actionList),
  );
}

function buttonCard(root: HTMLElement, b: Button, actionList: ActionInfo[]): HTMLElement {
  const isNew = b.id === 0;
  const name = h("input", { type: "text", value: b.name, maxLength: 40, required: true, ariaLabel: t("common.name") });
  const color = h("input", { type: "text", value: b.color, maxLength: 20, ariaLabel: t("buttons.color") });
  const gpio = h("input", { type: "number", value: String(b.gpio), min: "0", max: "27", ariaLabel: t("buttons.gpio") });
  const enabled = checkbox(b.enabled);
  const activeLow = checkbox(b.active_low);

  const known = new Set(actionList.map((a) => a.name));
  const rows = EVENTS.map((event) => {
    const label = t(`buttons.event.${event}`);
    const current = b.actions[event];
    const sel = h("select", { ariaLabel: t("buttons.actionOf", { event: label }) },
      h("option", { value: "" }, t("buttons.nothing")),
      ...actionList.map((a) => h("option", { value: a.name, selected: current?.action === a.name, title: a.description }, a.name)),
      current && !known.has(current.action)
        ? h("option", { value: current.action, selected: true }, t("buttons.notAvailable", { action: current.action }))
        : null,
    );
    const params = h("input", {
      type: "text", ariaLabel: t("buttons.paramsOf", { event: label }), placeholder: '{"change": 10}',
      value: current && Object.keys(current.parameters).length ? JSON.stringify(current.parameters) : "",
    });
    return { event, select: sel, params, row: h("div", { class: "map" }, h("span", {}, label), sel, params) };
  });

  return formCard(async (ev) => {
    ev.preventDefault();
    const payload: Record<string, unknown> = {};
    for (const r of rows) {
      if (!r.select.value) continue;
      try {
        payload[r.event] = { action: r.select.value, parameters: r.params.value.trim() ? JSON.parse(r.params.value) : {} };
      } catch {
        toast(t("buttons.badJson", { event: r.event }), true);
        return;
      }
    }
    const ok = await guard(() => api(isNew ? "POST" : "PUT", isNew ? "/api/buttons" : `/api/buttons/${b.id}`, {
      name: name.value.trim(), color: color.value.trim(), gpio: Number(gpio.value),
      active_low: activeLow.checked, enabled: enabled.checked, actions: payload,
    }), "common.saved");
    if (ok) await render(root);
  },
    h("h2", {}, isNew ? t("buttons.add") : b.name),
    fields(
      field(t("common.name"), name), field(t("buttons.color"), color), field(t("buttons.gpio"), gpio),
      toggle(t("common.enabled"), enabled),
      h("span", { title: t("buttons.activeLowHint") }, toggle(t("buttons.activeLow"), activeLow)),
    ),
    ...rows.map((r) => r.row),
    actions(
      submit(isNew ? t("common.add") : t("common.save")),
      isNew ? null : button(t("common.delete"), {
        variant: "danger",
        onclick: async (ev) => {
          if (!confirmTwice(ev.currentTarget as HTMLButtonElement)) return;
          if (await guard(() => api("DELETE", `/api/buttons/${b.id}`))) await render(root);
        },
      }),
    ),
  );
}
