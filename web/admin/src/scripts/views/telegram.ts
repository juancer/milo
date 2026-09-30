import { api } from "../api";
import { badge, clear, confirmTwice, errorMessage, field, h, toast, when } from "../dom";

interface TelegramStatus {
  enabled: boolean;
  configured: boolean;
  running: boolean;
  bot_username: string;
  poll_error?: string;
  pending_messages: number;
  urgent_messages: number;
}
interface Invitation {
  id: number;
  role: string;
  note: string;
  status: string;
  expires_at: string;
}
interface Request {
  id: number;
  telegram_id: number;
  username: string;
  first_name: string;
  role: string;
  created_at: string;
}
interface Message {
  id: number;
  direction: string;
  kind: string;
  type: string;
  status: string;
  person: string;
  created_at: string;
  attempts: number;
  last_error?: string;
}

let lastInvite: { link: string; command: string; expires_at: string } | null = null;

export async function render(root: HTMLElement): Promise<void> {
  const [st, invites, requests, messages] = await Promise.all([
    api<TelegramStatus>("GET", "/api/telegram"),
    api<Invitation[]>("GET", "/api/invitations"),
    api<Request[]>("GET", "/api/authorizations"),
    api<Message[]>("GET", "/api/messages"),
  ]);
  clear(root);
  const refresh = () => render(root);

  // ---- bot status and token
  const token = h("input", { type: "password", autocomplete: "off", placeholder: "123456:ABC-DEF…", ariaLabel: "Bot token" });
  const state = !st.enabled ? badge("feature off", true) : st.running ? badge("running") : badge("not running", true);
  root.append(h("section", { class: "card" }, h("h2", {}, "Telegram bot"),
    h("p", {}, state, " ", st.bot_username ? h("strong", {}, `@${st.bot_username}`) : null,
      st.enabled ? null : h("span", { class: "muted" }, " Turn the “telegram” feature on in the Dashboard.")),
    st.poll_error ? h("p", { class: "error" }, `Connection problem: ${st.poll_error}. Milo keeps retrying.`) : null,
    h("p", { class: "muted" },
      "Every installation has its own bot. Create one with @BotFather in Telegram and paste its token here. It is stored encrypted and never shown again."),
    h("form", {
      class: "inline",
      onsubmit: async (ev: Event) => {
        ev.preventDefault();
        try {
          await api("PUT", "/api/telegram/token", { token: token.value.trim() });
          token.value = "";
          toast("Token saved");
          await refresh();
        } catch (e) {
          toast(errorMessage(e), true);
        }
      },
    }, token, h("button", { type: "submit" }, st.configured ? "Replace token" : "Save token"),
      st.configured ? h("button", {
        type: "button", class: "danger",
        onclick: async (ev: Event) => {
          if (!confirmTwice(ev.currentTarget as HTMLButtonElement, "Remove token")) return;
          await api("DELETE", "/api/telegram/token");
          await refresh();
        },
      }, "Remove token") : null),
    h("p", { class: "muted" }, `${st.pending_messages} message(s) waiting for the child` + (st.urgent_messages ? `, ${st.urgent_messages} urgent` : "") + "."),
  ));

  // ---- requests waiting for approval
  root.append(h("section", { class: "card" }, h("h2", {}, "Authorization requests"),
    requests.length === 0 ? h("p", { class: "muted" }, "Nobody is waiting for approval.") : null,
    ...requests.map((r) => requestRow(r, refresh))));

  // ---- invitations
  const role = h("select", { ariaLabel: "Role" }, ...["PARENT", "FAMILY", "CHILD"].map((r) => h("option", { value: r, selected: r === "FAMILY" }, r)));
  const hours = h("input", { type: "number", value: "24", min: "1", max: "168", ariaLabel: "Valid for hours" });
  const note = h("input", { type: "text", maxLength: 60, placeholder: "e.g. Grandma", ariaLabel: "Note" });
  root.append(h("section", { class: "card" }, h("h2", {}, "Invite someone"),
    h("p", { class: "muted" }, "Create a single-use link and send it to the person. They open it in Telegram; you then approve them here. Nobody can join without an invitation and your approval."),
    h("form", {
      onsubmit: async (ev: Event) => {
        ev.preventDefault();
        try {
          const res = await api<{ link: string; command: string; expires_at: string }>("POST", "/api/invitations", {
            role: role.value, ttl_hours: Number(hours.value), note: note.value.trim(),
          });
          lastInvite = res;
          await refresh();
        } catch (e) {
          toast(errorMessage(e), true);
        }
      },
    }, h("div", { class: "fields" }, field("Role", role), field("Valid for (hours)", hours), field("Note", note),
      h("button", { type: "submit" }, "Create invitation"))),
    lastInvite ? h("div", { class: "card" },
      h("strong", {}, "Send this link now - it is shown only once"),
      h("input", { type: "text", readOnly: true, value: lastInvite.link || lastInvite.command, ariaLabel: "Invitation link", onfocus: (e: Event) => (e.target as HTMLInputElement).select() }),
      lastInvite.link ? null : h("p", { class: "muted" }, "The bot username is not known yet (save a valid token and enable Telegram). The person can send the command above to your bot instead."),
      h("p", { class: "muted" }, `Expires ${when(lastInvite.expires_at)}.`)) : null,
    h("div", { class: "table-wrap" }, h("table", {},
      h("thead", {}, h("tr", {}, ...["Role", "Note", "Status", "Expires", ""].map((t) => h("th", {}, t)))),
      h("tbody", {}, ...invites.map((i) => h("tr", {},
        h("td", {}, i.role), h("td", {}, i.note), h("td", {}, badge(i.status, i.status === "revoked")), h("td", {}, when(i.expires_at)),
        h("td", {}, i.status === "active" ? h("button", {
          type: "button", class: "secondary",
          onclick: async () => { await api("DELETE", `/api/invitations/${i.id}`); await refresh(); },
        }, "Revoke") : null))))))));

  // ---- message flow (no content)
  root.append(h("section", { class: "card" }, h("h2", {}, "Message activity"),
    h("p", { class: "muted" }, "Only who and when. Message content is never shown here."),
    messages.length === 0 ? h("p", { class: "muted" }, "No messages yet.") : null,
    h("ul", { class: "list" }, ...messages.map((m) => h("li", { class: "row" },
      h("span", {}, m.direction === "in" ? "⬇ from " : "⬆ to ", m.person || "?", " · ", m.kind, m.type === "emergency" ? badge("EMERGENCY", true) : null),
      h("span", { class: m.status === "failed" ? "error" : "muted" }, `${m.status}${m.last_error ? ` (${m.last_error})` : ""} · ${when(m.created_at)}`))))));
}

function requestRow(r: Request, refresh: () => Promise<void>): HTMLElement {
  const name = h("input", { type: "text", value: r.first_name, maxLength: 40, ariaLabel: "Display name" });
  const chat = h("input", { type: "checkbox", checked: r.role !== "CHILD" });
  return h("div", { class: "card" },
    h("strong", {}, "New authorization request"),
    h("p", {}, `${r.first_name || "(no name)"} · ${r.username ? `@${r.username}` : "no username"} · Telegram ID ${r.telegram_id} · role ${r.role}`),
    h("div", { class: "fields" }, field("Show as", name), h("label", { class: "check" }, chat, " Add as a chat the child can talk to")),
    h("div", { class: "actions" },
      h("button", {
        type: "button",
        onclick: async () => {
          try {
            await api("POST", `/api/authorizations/${r.id}/approve`, { display_name: name.value.trim(), create_chat: chat.checked });
            toast("Approved");
            await refresh();
          } catch (e) {
            toast(errorMessage(e), true);
          }
        },
      }, "Approve"),
      h("button", { type: "button", class: "danger", onclick: async () => { await api("POST", `/api/authorizations/${r.id}/reject`); await refresh(); } }, "Reject")));
}
