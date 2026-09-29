import { api, ApiError, type Status } from "./api";
import { clear, errorMessage, h } from "./dom";
import * as assistant from "./views/assistant";
import * as buttons from "./views/buttons";
import * as chats from "./views/chats";
import * as dashboard from "./views/dashboard";
import * as people from "./views/people";
import * as schedules from "./views/schedules";
import * as simulator from "./views/simulator";
import * as sound from "./views/sound";
import * as system from "./views/system";
import * as telegram from "./views/telegram";
import * as wakeword from "./views/wakeword";
import * as wifi from "./views/wifi";

interface SessionInfo {
  authenticated: boolean;
  setup_required: boolean;
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
  { id: "sound", title: "Sound", render: sound.render },
  { id: "wifi", title: "Wi-Fi", render: wifi.render },
  { id: "system", title: "System", render: system.render },
  { id: "simulator", title: "Simulator", render: simulator.render, stop: simulator.stop, devOnly: true },
];

const root = document.getElementById("view") as HTMLElement;
const nav = document.getElementById("nav") as HTMLElement;
const logout = document.getElementById("logout") as HTMLButtonElement;
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
    if (e instanceof ApiError && e.status === 401) return start();
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
  const password = h("input", { id: "password", type: "password", autocomplete: "current-password", required: true });
  const error = h("p", { class: "error", role: "alert", hidden: true });
  clear(root);
  root.append(h("section", { class: "card narrow" }, h("h2", {}, "Sign in"),
    h("form", {
      onsubmit: async (ev: Event) => {
        ev.preventDefault();
        error.hidden = true;
        try {
          await api("POST", "/api/login", { password: password.value });
          password.value = "";
          await start();
        } catch (e) {
          error.textContent = errorMessage(e);
          error.hidden = false;
        }
      },
    }, h("label", { for: "password" }, "Admin password"), password, error, h("button", { type: "submit" }, "Sign in"))));
}

function showSetup(): void {
  clear(nav);
  logout.hidden = true;
  clear(root);
  root.append(h("section", { class: "card" }, h("h2", {}, "Set the admin password"),
    h("p", {}, "For safety this is done on the device itself, so nobody else on the network can claim it. Run this on the Raspberry Pi, then reload:"),
    h("pre", {}, h("code", {}, "sudo -u nexo nexo admin-password"))));
}

async function start(): Promise<void> {
  try {
    const info = await api<SessionInfo>("GET", "/api/session");
    if (info.setup_required) return showSetup();
    if (!info.authenticated) return showLogin();
    const [status, sys] = await Promise.all([api<Status>("GET", "/api/status"), api<{ device_name: string }>("GET", "/api/system")]);
    isDev = status.hardware === "mock";
    const title = document.querySelector("header h1");
    if (title) title.textContent = sys.device_name;
    document.title = `${sys.device_name} admin`;
    logout.hidden = false;
    await route();
  } catch {
    clear(root);
    root.append(h("p", { class: "error", role: "alert" }, "Cannot reach the device."));
  }
}

logout.addEventListener("click", async () => {
  await api("POST", "/api/logout");
  active?.stop?.();
  await start();
});
window.addEventListener("hashchange", () => void route());
void start();
