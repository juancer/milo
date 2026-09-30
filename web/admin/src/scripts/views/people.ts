import { actions, badge, checkbox, field, fields, formCard, note, select, submit, toggle } from "../../components/ui";
import { t } from "../../i18n";
import { api } from "../api";
import { clear, h } from "../dom";
import { loadLocales } from "../locales";
import { guard } from "../ui";

interface User {
  id: number;
  display_name: string;
  role: string;
  active: boolean;
  language?: string; // "" = follow the device's notification language
  forward_incoming?: boolean; // opt-in copy of messages others leave for the device
  telegram_id: number;
  telegram_username: string;
  effective: Record<string, boolean>;
  overrides: Record<string, boolean>;
  role_defaults: string[];
}

const ROLES = ["OWNER", "PARENT", "FAMILY", "CHILD"];
const PERMISSIONS = [
  "send_message",
  "receive_message",
  "emergency_alert",
  "manage_contacts",
  "manage_configuration",
  "manage_features",
  "manage_schedules",
  "manage_credentials",
  "manage_users",
  "manage_invitations",
];

export async function render(root: HTMLElement): Promise<void> {
  const [users, locales] = await Promise.all([api<User[]>("GET", "/api/users"), loadLocales()]);
  clear(root);
  root.append(note(t("people.intro")), note(t("people.remoteHint")));
  if (users.length === 0) root.append(note(t("people.none")));
  for (const u of users) root.append(card(u, locales, () => render(root)));
}

function card(u: User, locales: { code: string; name: string }[], refresh: () => Promise<void>): HTMLElement {
  const name = h("input", { type: "text", value: u.display_name, maxLength: 40, ariaLabel: t("common.name") });
  const role = select(t("common.role"), ROLES.map((r) => [r, t(`role.${r}`)]), u.role);
  const language = select(t("people.language"), [["", t("people.languageDefault")], ...locales.map((l): [string, string] => [l.code, l.name])], u.language ?? "");
  const active = checkbox(u.active);
  const forward = checkbox(u.forward_incoming ?? false);
  const boxes = new Map<string, HTMLInputElement>();
  const marks = new Map<string, HTMLElement>();

  const defaultsFor = (r: string): Set<string> => (r === u.role ? new Set(u.role_defaults) : new Set(roleDefaultsGuess(r)));
  // Copies of incoming messages are only offered to people who can manage users.
  const forwardRow = toggle(t("people.forward"), forward);
  const syncMarks = () => {
    const defaults = defaultsFor(role.value);
    for (const p of PERMISSIONS) marks.get(p)!.hidden = boxes.get(p)!.checked === defaults.has(p);
    forwardRow.hidden = !boxes.get("manage_users")!.checked;
  };
  role.addEventListener("change", () => {
    // Switching role resets the boxes to that role's defaults.
    const defaults = defaultsFor(role.value);
    for (const p of PERMISSIONS) boxes.get(p)!.checked = defaults.has(p);
    syncMarks();
  });

  const grid = h("div", { class: "perms" }, ...PERMISSIONS.map((p) => {
    const box = checkbox(u.effective[p], { onchange: syncMarks });
    boxes.set(p, box);
    const mark = badge(t("people.custom"), true);
    marks.set(p, mark);
    return toggle(t(`perm.${p}`), box, mark);
  }));
  syncMarks();

  return formCard(async (ev) => {
    ev.preventDefault();
    const defaults = defaultsFor(role.value);
    const permissions: Record<string, boolean | null> = {};
    for (const p of PERMISSIONS) {
      const want = boxes.get(p)!.checked;
      permissions[p] = want === defaults.has(p) ? null : want; // null = follow the role
    }
    await guard(async () => {
      await api("PUT", `/api/users/${u.id}`, { display_name: name.value.trim(), role: role.value, active: active.checked, language: language.value, forward_incoming: forwardRow.hidden ? false : forward.checked, permissions });
      await refresh();
    }, "common.saved");
  },
    h("h2", {}, u.display_name, " ", u.active ? null : badge(t("people.deactivated"), true)),
    note(u.telegram_id ? t("people.telegramInfo", { username: u.telegram_username ? `@${u.telegram_username} · ` : "", id: u.telegram_id }) : t("people.noTelegram")),
    fields(field(t("common.name"), name), field(t("common.role"), role), field(t("people.language"), language), toggle(t("common.active"), active)),
    grid,
    forwardRow,
    note(t("people.forwardHint")),
    actions(submit(t("common.save"))));
}

// Mirrors internal/permissions role defaults, used only to preview a role change
// before saving. The server remains the source of truth.
function roleDefaultsGuess(role: string): string[] {
  switch (role) {
    case "OWNER":
      return PERMISSIONS;
    case "PARENT":
      return ["send_message", "receive_message", "emergency_alert", "manage_contacts", "manage_schedules"];
    case "FAMILY":
      return ["send_message", "receive_message"];
    default:
      return [];
  }
}
