import { badge, button, card, checkbox, emptyState, field, fields, list, note, row, toggle } from "../../components/ui";
import { t, tOr } from "../../i18n";
import { api } from "../api";
import { clear, h } from "../dom";
import { guard, toast } from "../ui";

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

const ORDER = [1, 2, 3, 4, 5, 6, 0]; // show the week starting Monday

const dayName = (d: number) => t(`day.${d}`);
const dayShort = (d: number) => t(`dayShort.${d}`);

export async function render(root: HTMLElement): Promise<void> {
  const data = await api<Response>("GET", "/api/schedules");
  clear(root);

  const tz = h("input", { type: "text", value: data.timezone, placeholder: "Europe/Madrid", ariaLabel: t("schedules.timezone") });
  root.append(
    card(t("schedules.timezone"),
      note(t("schedules.deviceTime", { time: data.local_time })),
      h("form", {
        class: "inline",
        onsubmit: async (ev: Event) => {
          ev.preventDefault();
          if (await guard(() => api("PUT", "/api/settings/timezone", { timezone: tz.value.trim() }), "schedules.timezoneSaved")) await render(root);
        },
      }, tz, h("button", { type: "submit" }, t("common.save")))),
    note(t("schedules.intro")),
    ...data.features.map((f) => featureCard(f, () => render(root))),
  );
  if (data.features.length === 0) root.append(emptyState(t("schedules.none")));
}

function featureCard(f: Schedule, refresh: () => Promise<void>): HTMLElement {
  const rules = f.rules.map((r) => ({ ...r }));
  const enabled = checkbox(f.enabled);
  const save = async (): Promise<void> => {
    if (await guard(() => api("PUT", `/api/schedules/${encodeURIComponent(f.feature)}`, { enabled: enabled.checked, rules }), "common.saved")) await refresh();
  };

  // ---- add windows: pick days and a time range
  const days = ORDER.map((d) => ({ d, box: checkbox(d >= 1 && d <= 5) }));
  const start = h("input", { type: "time", value: "08:00", ariaLabel: t("schedules.from") });
  const end = h("input", { type: "time", value: "20:00", ariaLabel: t("schedules.until") });

  const rows = rules
    .map((r, i) => ({ r, i }))
    .sort((a, b) => ORDER.indexOf(a.r.day_of_week) - ORDER.indexOf(b.r.day_of_week) || a.r.start.localeCompare(b.r.start))
    .map(({ r, i }) => row(
      h("span", {}, `${dayName(r.day_of_week)} ${r.start} - ${r.end}`, r.start > r.end ? h("span", { class: "muted" }, ` ${t("schedules.nextDay")}`) : null),
      button(t("common.remove"), { variant: "secondary", onclick: async () => { rules.splice(i, 1); await save(); } })));

  return card(null,
    h("h2", {}, tOr(`feature.${f.feature}`, f.feature), " ", f.enabled ? (f.open_now ? badge(t("schedules.openNow")) : badge(t("schedules.closedNow"), true)) : badge(t("schedules.alwaysAvailable"))),
    toggle(t("schedules.limit"), enabled, button(t("schedules.apply"), { variant: "secondary", onclick: save })),
    f.enabled && rules.length === 0 ? h("p", { class: "error" }, t("schedules.blocked")) : null,
    list(...rows),
    fields(
      h("div", { class: "days" }, ...days.map(({ d, box }) => toggle(dayShort(d), box))),
      field(t("schedules.from"), start), field(t("schedules.until"), end),
      button(t("schedules.addHours"), {
        onclick: async () => {
          const picked = days.filter(({ box }) => box.checked).map(({ d }) => d);
          if (picked.length === 0 || !start.value || !end.value) return toast(t("schedules.pickDays"), true);
          for (const d of picked) rules.push({ day_of_week: d, start: start.value, end: end.value, enabled: true });
          enabled.checked = true;
          await save();
        },
      })),
    note(t("schedules.help")));
}

