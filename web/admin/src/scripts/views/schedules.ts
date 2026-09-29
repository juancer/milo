import { api } from "../api";
import { badge, clear, errorMessage, field, h, toast } from "../dom";

interface Rule {
  day_of_week: number;
  start: string;
  end: string;
  enabled: boolean;
}
interface Schedule {
  feature: string;
  enabled: boolean;
  open_now: boolean;
  rules: Rule[];
}
interface Response {
  timezone: string;
  local_time: string;
  features: Schedule[];
}

const DAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
const ORDER = [1, 2, 3, 4, 5, 6, 0]; // show the week starting Monday

export async function render(root: HTMLElement): Promise<void> {
  const data = await api<Response>("GET", "/api/schedules");
  clear(root);

  const tz = h("input", { type: "text", value: data.timezone, placeholder: "Europe/Madrid", ariaLabel: "Timezone" });
  root.append(
    h("section", { class: "card" }, h("h2", {}, "Timezone"),
      h("p", { class: "muted" }, `Device time now: ${data.local_time}. Schedules follow this timezone.`),
      h("form", {
        class: "inline",
        onsubmit: async (ev: Event) => {
          ev.preventDefault();
          try {
            await api("PUT", "/api/settings/timezone", { timezone: tz.value.trim() });
            toast("Timezone saved");
            await render(root);
          } catch (e) {
            toast(errorMessage(e), true);
          }
        },
      }, tz, h("button", { type: "submit" }, "Save"))),
    h("p", { class: "muted" },
      "Choose when each feature may be used. Messages that arrive outside the allowed hours are kept, not deleted, and announced when the hours begin. Emergency alerts always go through."),
    ...data.features.map((f) => card(f, () => render(root))),
  );
  if (data.features.length === 0) root.append(h("p", { class: "muted" }, "No features to schedule."));
}

function card(f: Schedule, refresh: () => Promise<void>): HTMLElement {
  const rules = f.rules.map((r) => ({ ...r }));
  const enabled = h("input", { type: "checkbox", checked: f.enabled });
  const save = async (): Promise<void> => {
    try {
      await api("PUT", `/api/schedules/${encodeURIComponent(f.feature)}`, { enabled: enabled.checked, rules });
      toast("Saved");
      await refresh();
    } catch (e) {
      toast(errorMessage(e), true);
    }
  };

  // ---- add windows: pick days and a time range
  const days = ORDER.map((d) => ({ d, box: h("input", { type: "checkbox", checked: d >= 1 && d <= 5 }) }));
  const start = h("input", { type: "time", value: "08:00", ariaLabel: "From" });
  const end = h("input", { type: "time", value: "20:00", ariaLabel: "Until" });

  const list = rules
    .map((r, i) => ({ r, i }))
    .sort((a, b) => ORDER.indexOf(a.r.day_of_week) - ORDER.indexOf(b.r.day_of_week) || a.r.start.localeCompare(b.r.start))
    .map(({ r, i }) => h("li", { class: "row" },
      h("span", {}, `${DAYS[r.day_of_week]} ${r.start} - ${r.end}`, r.start > r.end ? h("span", { class: "muted" }, " (into next day)") : null),
      h("button", { type: "button", class: "secondary", onclick: async () => { rules.splice(i, 1); await save(); } }, "Remove")));

  return h("section", { class: "card" },
    h("h2", {}, f.feature, " ", f.enabled ? (f.open_now ? badge("open now") : badge("closed now", true)) : badge("always available")),
    h("label", { class: "check" }, enabled, " Limit this feature to the hours below",
      h("button", { type: "button", class: "secondary", onclick: save }, "Apply")),
    f.enabled && rules.length === 0 ? h("p", { class: "error" }, "No hours are set, so this feature is blocked all the time.") : null,
    h("ul", { class: "list" }, ...list),
    h("div", { class: "fields" },
      h("div", { class: "days" }, ...days.map(({ d, box }) => h("label", { class: "check" }, box, ` ${DAYS[d].slice(0, 3)}`))),
      field("From", start), field("Until", end),
      h("button", {
        type: "button",
        onclick: async () => {
          const picked = days.filter(({ box }) => box.checked).map(({ d }) => d);
          if (picked.length === 0 || !start.value || !end.value) return toast("Pick at least one day and both times", true);
          for (const d of picked) rules.push({ day_of_week: d, start: start.value, end: end.value, enabled: true });
          enabled.checked = true;
          await save();
        },
      }, "Add hours")),
    h("p", { class: "muted" }, "Until 00:00 means midnight. A window that ends earlier than it starts (22:00 - 06:00) continues into the next day."));
}
