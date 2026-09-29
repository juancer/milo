import { api } from "../api";
import { badge, clear, confirmTwice, errorMessage, field, h, toast, when } from "../dom";

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
  const current = h("input", { type: "password", autocomplete: "current-password", required: true, ariaLabel: "Current password" });
  const next = h("input", { type: "password", autocomplete: "new-password", required: true, minLength: 12, ariaLabel: "New password" });
  root.append(h("form", {
    class: "card",
    onsubmit: async (ev: Event) => {
      ev.preventDefault();
      try {
        await api("POST", "/api/account/password", { current_password: current.value, new_password: next.value });
        current.value = next.value = "";
        toast("Password changed");
      } catch (e) {
        toast(errorMessage(e), true);
      }
    },
  },
    h("h2", {}, `Your account (${me.username})`),
    h("p", { class: "muted" }, "Choose a long password only you know (at least 12 characters). Changing it signs you out of other browsers."),
    h("div", { class: "fields" }, field("Current password", current), field("New password", next), h("button", { type: "submit" }, "Change password"))));

  // ---- one-time reveal of a generated password
  if (revealed) {
    const r = revealed;
    root.append(h("section", { class: "card" },
      h("h2", {}, `Password for ${r.username}`),
      h("p", {}, "Give this to them now. It is shown only once, and they must choose their own when they first sign in."),
      h("input", { type: "text", readOnly: true, value: r.password, ariaLabel: "Generated password", onfocus: (e: Event) => (e.target as HTMLInputElement).select() }),
      h("div", { class: "actions" }, h("button", { type: "button", class: "secondary", onclick: () => { revealed = null; void refresh(); } }, "I have written it down"))));
  }

  // ---- people who can manage the device
  root.append(h("section", { class: "card" }, h("h2", {}, "Administrators"),
    h("p", { class: "muted" }, "Everyone listed here can manage the device from this page. Only the owner can add, reset or remove them."),
    h("ul", { class: "list" }, ...admins.map((a) => h("li", { class: "row" },
      h("span", {}, h("strong", {}, a.username), " ", a.is_owner ? badge("owner") : null, a.you ? badge("you") : null, a.must_change_password ? badge("must choose a password", true) : null),
      h("span", { class: "muted" }, a.last_login ? `last seen ${when(a.last_login)}` : "never signed in"),
      me.is_owner && !a.you ? h("span", { class: "actions" },
        h("button", { type: "button", class: "secondary", onclick: async (ev: Event) => {
          if (!confirmTwice(ev.currentTarget as HTMLButtonElement, "Reset password")) return;
          try {
            const res = await api<{ username: string; generated_password: string }>("POST", `/api/admins/${a.id}/reset`);
            revealed = { username: res.username, password: res.generated_password };
            await refresh();
          } catch (e) { toast(errorMessage(e), true); }
        } }, "Reset password"),
        !a.is_owner ? h("button", { type: "button", class: "danger", onclick: async (ev: Event) => {
          if (!confirmTwice(ev.currentTarget as HTMLButtonElement, "Remove")) return;
          try { await api("DELETE", `/api/admins/${a.id}`); await refresh(); } catch (e) { toast(errorMessage(e), true); }
        } }, "Remove") : null) : null)))));

  if (me.is_owner) {
    const name = h("input", { type: "text", required: true, maxLength: 24, pattern: "[a-z][a-z0-9_-]{1,23}", placeholder: "mom", autocapitalize: "none", ariaLabel: "Username" });
    root.append(h("form", {
      class: "card",
      onsubmit: async (ev: Event) => {
        ev.preventDefault();
        try {
          const res = await api<{ username: string; generated_password: string }>("POST", "/api/admins", { username: name.value.trim() });
          revealed = { username: res.username, password: res.generated_password };
          await refresh();
        } catch (e) {
          toast(errorMessage(e), true);
        }
      },
    },
      h("h2", {}, "Add an administrator"),
      h("p", { class: "muted" }, "For example mom, dad or grandma. A password is generated and shown once; they choose their own at first sign-in."),
      h("div", { class: "fields" }, field("Username (lowercase letters, digits, - _)", name), h("button", { type: "submit" }, "Add"))));
  }
}
