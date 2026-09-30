import { api, ApiError, type Status } from "./api";
import { clear, errorMessage, h } from "./dom";
import * as admins from "./views/admins";
import * as assistant from "./views/assistant";
import * as backup from "./views/backup";
import * as buttons from "./views/buttons";
import * as chats from "./views/chats";
import * as dashboard from "./views/dashboard";
import * as people from "./views/people";
import * as schedules from "./views/schedules";
import * as simulator from "./views/simulator";
import * as skills from "./views/skills";
import * as sound from "./views/sound";
import * as system from "./views/system";
import * as telegram from "./views/telegram";
import * as wakeword from "./views/wakeword";
import * as wifi from "./views/wifi";

interface SessionInfo {
  authenticated: boolean;
  username?: string;
  is_owner?: boolean;
  must_change_password?: boolean;
}
interface View {
  id: string;
  title: string;
  render: (root: HTMLElement) => Promise<void>;
  stop?: () => void;
  devOnly?: boolean;
}

// Later phases add their views here.
const VIEWS: View[] = [
  { id: "dashboard", title: "Dashboard", render: dashboard.render },
  { id: "buttons", title: "Buttons", render: buttons.render },
  { id: "telegram", title: "Telegram", render: telegram.render },
  { id: "people", title: "People", render: people.render },
  { id: "chats", title: "Chats", render: chats.render },
  { id: "schedules", title: "Schedules", render: schedules.render },
  { id: "assistant", title: "Assistant", render: assistant.render },
  { id: "wakeword", title: "Wake word", render: wakeword.render, stop: wakeword.stop },
  { id: "skills", title: "Skills", render: skills.render, stop: skills.stop },
  { id: "sound", title: "Sound", render: sound.render },
  { id: "wifi", title: "Wi-Fi", render: wifi.render },
  { id: "system", title: "System", render: system.render },
  { id: "backup", title: "Backup", render: backup.render },
  { id: "admins", title: "Admins", render: admins.render },
  { id: "simulator", title: "Simulator", render: simulator.render, stop: simulator.stop, devOnly: true },
];

const root = document.getElementById("view") as HTMLElement;
const nav = document.getElementById("nav") as HTMLElement;
const logout = document.getElementById("logout") as HTMLButtonElement;
const signedIn = document.getElementById("signed-in") as HTMLElement;
let active: View | undefined;
let isDev = false;

function visibleViews(): View[] {
  return VIEWS.filter((v) => !v.devOnly || isDev);
}

async function route(): Promise<void> {
  const wanted = location.hash.slice(1) || "dashboard";
  const view = visibleViews().find((v) => v.id === wanted) ?? visibleViews()[0];
  active?.stop?.();
  active = view;
  renderNav();
  clear(root);
  root.append(h("p", { class: "muted" }, "Loading…"));
  try {
    const next = h("div", { class: "stack" });
    await view.render(next);
    clear(root);
    root.append(next);
  } catch (e) {
    if (e instanceof ApiError && (e.status === 401 || (e.status === 403 && e.message === "password_change_required"))) return start();
    clear(root);
    root.append(h("p", { class: "error", role: "alert" }, errorMessage(e)));
  }
}

function renderNav(): void {
  clear(nav);
  for (const v of visibleViews()) {
    nav.append(h("a", { href: `#${v.id}`, class: v === active ? "tab active" : "tab", ariaCurrent: v === active ? "page" : undefined }, v.title));
  }
}

function showLogin(): void {
  clear(nav);
  logout.hidden = true;
  const user = h("input", { id: "username", type: "text", autocomplete: "username", autocapitalize: "none", required: true });
  const password = h("input", { id: "password", type: "password", autocomplete: "current-password", required: true });
  const error = h("p", { class: "error", role: "alert", hidden: true });
  clear(root);
  root.append(h("section", { class: "card narrow" }, h("h2", {}, "Sign in"),
    h("form", {
      onsubmit: async (ev: Event) => {
        ev.preventDefault();
        error.hidden = true;
        try {
          await api("POST", "/api/login", { username: user.value.trim(), password: password.value });
          password.value = "";
          await start();
        } catch (e) {
          error.textContent = errorMessage(e);
          error.hidden = false;
        }
      },
    }, h("label", { for: "username" }, "Username"), user, h("label", { for: "password" }, "Password"), password, error, h("button", { type: "submit" }, "Sign in")),
    h("p", { class: "muted" }, "First time? The username and password were shown once when Milo was installed.")));
}

/** Shown right after signing in with a generated password: nothing else works until it is changed. */
function showForcedChange(username: string): void {
  clear(nav);
  logout.hidden = false;
  const current = h("input", { id: "current", type: "password", autocomplete: "current-password", required: true });
  const next = h("input", { id: "next", type: "password", autocomplete: "new-password", required: true, minLength: 12 });
  const error = h("p", { class: "error", role: "alert", hidden: true });
  clear(root);
  root.append(h("section", { class: "card narrow" }, h("h2", {}, `Welcome, ${username}`),
    h("p", {}, "Choose your own password before you continue: at least 12 characters, known only to you."),
    h("form", {
      onsubmit: async (ev: Event) => {
        ev.preventDefault();
        error.hidden = true;
        try {
          await api("POST", "/api/account/password", { current_password: current.value, new_password: next.value });
          await start();
        } catch (e) {
          error.textContent = errorMessage(e);
          error.hidden = false;
        }
      },
    }, h("label", { for: "current" }, "Password you were given"), current, h("label", { for: "next" }, "Your new password"), next, error, h("button", { type: "submit" }, "Save and continue"))));
}

async function start(): Promise<void> {
  try {
    const info = await api<SessionInfo>("GET", "/api/session");
    if (!info.authenticated) return showLogin();
    if (info.must_change_password) return showForcedChange(info.username ?? "");
    const [status, sys] = await Promise.all([api<Status>("GET", "/api/status"), api<{ device_name: string }>("GET", "/api/system")]);
    isDev = status.hardware === "mock";
    const title = document.querySelector("header h1");
    if (title) title.textContent = sys.device_name;
    document.title = `${sys.device_name} admin`;
    logout.hidden = false;
    signedIn.textContent = info.username ? `${info.username}${info.is_owner ? " (owner)" : ""}` : "";
    await route();
  } catch {
    clear(root);
    root.append(h("p", { class: "error", role: "alert" }, "Cannot reach the device."));
  }
}

logout.addEventListener("click", async () => {
  await api("POST", "/api/logout");
  signedIn.textContent = "";
  active?.stop?.();
  await start();
});
window.addEventListener("hashchange", () => void route());
void start();
