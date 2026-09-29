import { api } from "../api";
import { badge, clear, errorMessage, field, h, toast } from "../dom";

interface User {
  id: number;
  display_name: string;
  role: string;
  active: boolean;
  telegram_id: number;
  telegram_username: string;
  effective: Record<string, boolean>;
  overrides: Record<string, boolean>;
  role_defaults: string[];
}

const ROLES = ["OWNER", "PARENT", "FAMILY", "CHILD"];
const PERMISSIONS: [string, string][] = [
  ["send_message", "Send messages to the device"],
  ["receive_message", "Receive messages from the child"],
  ["emergency_alert", "Send emergency alerts"],
  ["manage_contacts", "Manage contacts"],
  ["manage_configuration", "Manage configuration"],
  ["manage_features", "Manage features"],
  ["manage_schedules", "Manage schedules"],
  ["manage_credentials", "Manage credentials"],
  ["manage_users", "Manage users"],
  ["manage_invitations", "Manage invitations"],
];

export async function render(root: HTMLElement): Promise<void> {
  const users = await api<User[]>("GET", "/api/users");
  clear(root);
  root.append(h("p", { class: "muted" },
    "Each role has default permissions. Tick or untick a box to give one person more or less than their role; “custom” marks the differences. Deactivating someone blocks them immediately."));
  if (users.length === 0) root.append(h("p", { class: "muted" }, "Nobody is authorized yet. Invite someone from the Telegram tab."));
  for (const u of users) root.append(card(u, () => render(root)));
}

function card(u: User, refresh: () => Promise<void>): HTMLElement {
  const name = h("input", { type: "text", value: u.display_name, maxLength: 40, ariaLabel: "Name" });
  const role = h("select", { ariaLabel: "Role" }, ...ROLES.map((r) => h("option", { value: r, selected: r === u.role }, r)));
  const active = h("input", { type: "checkbox", checked: u.active });
  const boxes = new Map<string, HTMLInputElement>();
  const marks = new Map<string, HTMLElement>();

  const defaultsFor = (r: string): Set<string> => (r === u.role ? new Set(u.role_defaults) : new Set(roleDefaultsGuess(r)));
  const syncMarks = () => {
    const defaults = defaultsFor(role.value);
    for (const [p] of PERMISSIONS) {
      const custom = boxes.get(p)!.checked !== defaults.has(p);
      marks.get(p)!.hidden = !custom;
    }
  };
  role.addEventListener("change", () => {
    // Switching role resets the boxes to that role's defaults.
    const defaults = defaultsFor(role.value);
    for (const [p] of PERMISSIONS) boxes.get(p)!.checked = defaults.has(p);
    syncMarks();
  });

  const grid = h("div", { class: "perms" }, ...PERMISSIONS.map(([p, label]) => {
    const box = h("input", { type: "checkbox", checked: u.effective[p], onchange: syncMarks });
    boxes.set(p, box);
    const mark = badge("custom", true);
    marks.set(p, mark);
    return h("label", { class: "check" }, box, ` ${label} `, mark);
  }));
  syncMarks();

  return h("form", {
    class: "card",
    onsubmit: async (ev: Event) => {
      ev.preventDefault();
      const defaults = defaultsFor(role.value);
      const permissions: Record<string, boolean | null> = {};
      for (const [p] of PERMISSIONS) {
        const want = boxes.get(p)!.checked;
        permissions[p] = want === defaults.has(p) ? null : want; // null = follow the role
      }
      try {
        await api("PUT", `/api/users/${u.id}`, { display_name: name.value.trim(), role: role.value, active: active.checked, permissions });
        toast("Saved");
        await refresh();
      } catch (e) {
        toast(errorMessage(e), true);
      }
    },
  },
    h("h2", {}, u.display_name, " ", u.active ? null : badge("deactivated", true)),
    h("p", { class: "muted" }, u.telegram_id ? `Telegram ${u.telegram_username ? `@${u.telegram_username} · ` : ""}ID ${u.telegram_id}` : "No Telegram account"),
    h("div", { class: "fields" }, field("Name", name), field("Role", role), h("label", { class: "check" }, active, " Active")),
    grid,
    h("div", { class: "actions" }, h("button", { type: "submit" }, "Save")));
}

// Mirrors internal/permissions role defaults, used only to preview a role change
// before saving. The server remains the source of truth.
function roleDefaultsGuess(role: string): string[] {
  switch (role) {
    case "OWNER":
      return PERMISSIONS.map(([p]) => p);
    case "PARENT":
      return ["send_message", "receive_message", "emergency_alert", "manage_contacts", "manage_schedules"];
    case "FAMILY":
      return ["send_message", "receive_message"];
    default:
      return [];
  }
}
