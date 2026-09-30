import { actions, button, field, fields, formCard, note, select, submit, toggle, checkbox } from "../../components/ui";
import { t } from "../../i18n";
import { api } from "../api";
import { clear, h } from "../dom";
import { confirmTwice, guard } from "../ui";

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
  root.append(note(t("chats.intro")));
  for (const c of chats) root.append(card(c, users, () => render(root)));
  root.append(card({ id: 0, name: "", kind: "user", user_id: users[0]?.id ?? 0, telegram_chat_id: 0, favorite: true, position: chats.length + 1, enabled: true }, users, () => render(root)));
}

function card(c: Chat, users: User[], refresh: () => Promise<void>): HTMLElement {
  const isNew = c.id === 0;
  const name = h("input", { type: "text", value: c.name, maxLength: 40, required: true, ariaLabel: t("common.name") });
  const kind = select(t("chats.type"), [["user", t("chats.kindUser")], ["group", t("chats.kindGroup")]], c.kind);
  const user = select(t("chats.person"), users.filter((u) => u.telegram_id).map((u) => [String(u.id), u.display_name]), String(c.user_id));
  const group = h("input", { type: "number", value: c.kind === "group" ? String(c.telegram_chat_id) : "", placeholder: "-1001234567890", ariaLabel: t("chats.groupId") });
  const position = h("input", { type: "number", value: String(c.position), min: "0", max: "1000", ariaLabel: t("chats.order") });
  const favorite = checkbox(c.favorite);
  const enabled = checkbox(c.enabled);
  const userField = field(t("chats.person"), user);
  const groupField = field(t("chats.groupId"), group);
  const sync = () => {
    userField.hidden = kind.value !== "user";
    groupField.hidden = kind.value !== "group";
  };
  kind.addEventListener("change", sync);
  sync();

  return formCard(async (ev) => {
    ev.preventDefault();
    await guard(async () => {
      await api(isNew ? "POST" : "PUT", isNew ? "/api/chats" : `/api/chats/${c.id}`, {
        name: name.value.trim(), kind: kind.value, user_id: Number(user.value) || 0,
        telegram_chat_id: Number(group.value) || 0, favorite: favorite.checked, position: Number(position.value), enabled: enabled.checked,
      });
      await refresh();
    }, "common.saved");
  },
    h("h2", {}, isNew ? t("chats.add") : c.name),
    fields(field(t("chats.nameLabel"), name), field(t("chats.type"), kind), userField, groupField, field(t("chats.order"), position),
      toggle(t("chats.favorite"), favorite), toggle(t("common.enabled"), enabled)),
    actions(submit(isNew ? t("common.add") : t("common.save")),
      isNew ? null : button(t("common.delete"), {
        variant: "danger",
        onclick: async (ev) => {
          if (!confirmTwice(ev.currentTarget as HTMLButtonElement)) return;
          if (await guard(() => api("DELETE", `/api/chats/${c.id}`))) await refresh();
        },
      })));
}
