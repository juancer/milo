import { actions, badge, button, card, field, fields, formCard, list, note, row, submit } from "../../components/ui";
import { t } from "../../i18n";
import { api } from "../api";
import { clear, h, when } from "../dom";
import { confirmDialog, confirmTwice, guard } from "../ui";

interface Admin {
  id: number;
  username: string;
  is_owner: boolean;
  must_change_password: boolean;
  created_at: string;
  last_login?: string;
  you: boolean;
}

let revealed: { username: string; password: string } | null = null;

export async function render(root: HTMLElement): Promise<void> {
  const [admins, me] = await Promise.all([
    api<Admin[]>("GET", "/api/admins"),
    api<{ username: string; is_owner: boolean }>("GET", "/api/session"),
  ]);
  clear(root);
  const refresh = () => render(root);

  // ---- own password
  const current = h("input", { type: "password", autocomplete: "current-password", required: true, ariaLabel: t("admins.currentPassword") });
  const next = h("input", { type: "password", autocomplete: "new-password", required: true, minLength: 12, ariaLabel: t("admins.newPassword") });
  root.append(formCard(async (ev) => {
    ev.preventDefault();
    await guard(async () => {
      await api("POST", "/api/account/password", { current_password: current.value, new_password: next.value });
      current.value = next.value = "";
    }, "admins.passwordChanged");
  },
    h("h2", {}, t("admins.yourAccount", { username: me.username })),
    note(t("admins.passwordHelp")),
    fields(field(t("admins.currentPassword"), current), field(t("admins.newPassword"), next), submit(t("admins.changePassword")))));

  // ---- one-time reveal of a generated password
  if (revealed) {
    const r = revealed;
    root.append(card(t("admins.passwordFor", { username: r.username }),
      h("p", {}, t("admins.revealHelp")),
      h("input", { type: "text", readOnly: true, value: r.password, ariaLabel: t("admins.generatedPassword"), onfocus: (e: Event) => (e.target as HTMLInputElement).select() }),
      actions(button(t("admins.writtenDown"), { variant: "secondary", onclick: () => { revealed = null; void refresh(); } }))));
  }

  // ---- people who can manage the device
  const reveal = (res: { username: string; generated_password: string }) => {
    revealed = { username: res.username, password: res.generated_password };
  };
  root.append(card(t("admins.title"),
    note(t("admins.help")),
    list(...admins.map((a) => row(
      h("span", {}, h("strong", {}, a.username), " ", a.is_owner ? badge(t("admins.owner")) : null, a.you ? badge(t("admins.you")) : null, a.must_change_password ? badge(t("admins.mustChoose"), true) : null),
      h("span", { class: "muted" }, a.last_login ? t("admins.lastSeen", { when: when(a.last_login) }) : t("admins.neverSignedIn")),
      me.is_owner && !a.you ? actions(
        button(t("admins.resetPassword"), { variant: "secondary", onclick: async (ev) => {
          if (!confirmTwice(ev.currentTarget as HTMLButtonElement)) return;
          if (await guard(async () => reveal(await api("POST", `/api/admins/${a.id}/reset`)))) await refresh();
        } }),
        !a.is_owner ? button(t("common.remove"), { variant: "danger", onclick: async () => {
          if (!(await confirmDialog(t("admins.confirmRemove", { username: a.username }), t("common.remove")))) return;
          if (await guard(() => api("DELETE", `/api/admins/${a.id}`))) await refresh();
        } }) : null) : null)))));

  if (me.is_owner) {
    const name = h("input", { type: "text", required: true, maxLength: 24, pattern: "[a-z][a-z0-9_-]{1,23}", placeholder: t("admins.usernameExample"), autocapitalize: "none", ariaLabel: t("login.username") });
    root.append(formCard(async (ev) => {
      ev.preventDefault();
      if (await guard(async () => reveal(await api("POST", "/api/admins", { username: name.value.trim() })))) await refresh();
    },
      h("h2", {}, t("admins.add")),
      note(t("admins.addHelp")),
      fields(field(t("admins.usernameHelp"), name), submit(t("common.add")))));
  }
}
