import { api } from "../api";
import { clear, confirmTwice, errorMessage, field, h, toast } from "../dom";

interface Chat {
  id: number;
  name: string;
  kind: string;
  user_id: number;
  telegram_chat_id: number;
  favorite: boolean;
  position: number;
  enabled: boolean;
}
interface User {
  id: number;
  display_name: string;
  telegram_id: number;
}

export async function render(root: HTMLElement): Promise<void> {
  const [chats, users] = await Promise.all([api<Chat[]>("GET", "/api/chats"), api<User[]>("GET", "/api/users")]);
  clear(root);
  root.append(h("p", { class: "muted" },
    "Chats are who the child can send voice messages to. The CHAT button cycles through the favorites in this order. A private chat always goes to an authorized person's own account."));
  for (const c of chats) root.append(card(c, users, () => render(root)));
  root.append(card({ id: 0, name: "", kind: "user", user_id: users[0]?.id ?? 0, telegram_chat_id: 0, favorite: true, position: chats.length + 1, enabled: true }, users, () => render(root)));
}

function card(c: Chat, users: User[], refresh: () => Promise<void>): HTMLElement {
  const isNew = c.id === 0;
  const name = h("input", { type: "text", value: c.name, maxLength: 40, required: true, ariaLabel: "Name" });
  const kind = h("select", { ariaLabel: "Type" },
    h("option", { value: "user", selected: c.kind === "user" }, "Private (a person)"),
    h("option", { value: "group", selected: c.kind === "group" }, "Telegram group"));
  const user = h("select", { ariaLabel: "Person" }, ...users.filter((u) => u.telegram_id).map((u) => h("option", { value: String(u.id), selected: u.id === c.user_id }, u.display_name)));
  const group = h("input", { type: "number", value: c.kind === "group" ? String(c.telegram_chat_id) : "", placeholder: "-1001234567890", ariaLabel: "Group chat id" });
  const position = h("input", { type: "number", value: String(c.position), min: "0", max: "1000", ariaLabel: "Order" });
  const favorite = h("input", { type: "checkbox", checked: c.favorite });
  const enabled = h("input", { type: "checkbox", checked: c.enabled });
  const userField = field("Person", user);
  const groupField = field("Group chat id", group);
  const sync = () => {
    userField.hidden = kind.value !== "user";
    groupField.hidden = kind.value !== "group";
  };
  kind.addEventListener("change", sync);
  sync();

  return h("form", {
    class: "card",
    onsubmit: async (ev: Event) => {
      ev.preventDefault();
      try {
        await api(isNew ? "POST" : "PUT", isNew ? "/api/chats" : `/api/chats/${c.id}`, {
          name: name.value.trim(), kind: kind.value, user_id: Number(user.value) || 0,
          telegram_chat_id: Number(group.value) || 0, favorite: favorite.checked, position: Number(position.value), enabled: enabled.checked,
        });
        toast("Saved");
        await refresh();
      } catch (e) {
        toast(errorMessage(e), true);
      }
    },
  },
    h("h2", {}, isNew ? "Add a chat" : c.name),
    h("div", { class: "fields" }, field("Name (spoken/shown)", name), field("Type", kind), userField, groupField, field("Order", position),
      h("label", { class: "check" }, favorite, " Favorite (CHAT button)"), h("label", { class: "check" }, enabled, " Enabled")),
    h("div", { class: "actions" }, h("button", { type: "submit" }, isNew ? "Add" : "Save"),
      isNew ? null : h("button", {
        type: "button", class: "danger",
        onclick: async (ev: Event) => {
          if (!confirmTwice(ev.currentTarget as HTMLButtonElement, "Delete")) return;
          await api("DELETE", `/api/chats/${c.id}`);
          await refresh();
        },
      }, "Delete")));
}
