import { applyI18n, getLocale, localeList, setLocale, storedLocale, t } from "../i18n";
import { alert, loadingState } from "../components/ui";
import { api, ApiError, type Status } from "./api";
import { clear, errorMessage, h } from "./dom";
import { renderHome } from "./home";
import { featureStrip, type Feature } from "./featureState";
import { sectionById, type Section } from "./sections";
import { fillSheet, hideSheet, initSheet, isSheetOpen, showSheet } from "./sheet";

interface SessionInfo {
  authenticated: boolean;
  username?: string;
  is_owner?: boolean;
  must_change_password?: boolean;
  setup_required?: boolean;
}
const root = document.getElementById("view") as HTMLElement;
const logout = document.getElementById("logout") as HTMLButtonElement;
const signedIn = document.getElementById("signed-in") as HTMLElement;
let active: Section | undefined;
let homeRoot: HTMLElement | null = null;
let isDev = false;
let session: SessionInfo = { authenticated: false };
let deviceName = "Milo";

/** Copies one of the static <template> screens rendered by AdminLayout.astro. */
function fromTemplate(id: string): DocumentFragment {
  const tpl = document.getElementById(id) as HTMLTemplateElement;
  const frag = tpl.content.cloneNode(true) as DocumentFragment;
  applyI18n(frag);
  return frag;
}

function show(node: Node): void {
  clear(root);
  root.append(node);
}

const visible = (s: Section | undefined): Section | undefined => (s && (!s.devOnly || isDev) ? s : undefined);

/** Points the address bar at a section (a new history entry, so Back closes the sheet) and opens it. */
function openSection(id: string): void {
  if (location.hash.slice(1) !== id) history.pushState({ sheet: true }, "", `#${id}`);
  void route();
}

/** Closes the sheet: Back if we opened it ourselves, otherwise clear the hash without adding history. */
function dismissSheet(): void {
  if (history.state?.sheet) history.back();
  else {
    history.replaceState(null, "", location.pathname + location.search);
    void route();
  }
}

async function paintHome(): Promise<void> {
  if (!homeRoot || !homeRoot.isConnected) {
    homeRoot = h("div", {});
    show(homeRoot);
    homeRoot.append(loadingState());
  }
  await renderHome(homeRoot, openSection, isDev);
}

/** Renders one section into the sheet: its feature switches first, then the view itself. */
async function paintSheet(section: Section): Promise<void> {
  const title = t(`nav.${section.id}`);
  showSheet(title, section.icon, section.tone, section.id).append(loadingState());
  try {
    const page = h("div", { class: "stack" });
    if (section.features) {
      const all = await api<Feature[]>("GET", "/api/features");
      const strip = featureStrip(all.filter((f) => section.features?.includes(f.name)), () => void paintSheet(section));
      if (strip) page.append(strip);
    }
    const view = h("div", { class: "stack" });
    await section.render(view);
    page.append(view);
    if (active === section) fillSheet(page);
  } catch (e) {
    if (e instanceof ApiError && (e.status === 401 || (e.status === 403 && e.message === "password_change_required"))) return void start();
    if (active === section) fillSheet(h("div", {}, alert(errorMessage(e))));
  }
}

/** The address decides what is on screen: Home always, plus a sheet when the hash names a section. */
async function route(): Promise<void> {
  const wanted = visible(sectionById(location.hash.slice(1)));
  const previous = active;
  if (previous && previous !== wanted) previous.stop?.();
  active = wanted;
  if (!wanted) {
    const wasOpen = isSheetOpen();
    hideSheet();
    if (wasOpen || !homeRoot?.isConnected) await paintHome();
    else void paintHome(); // returning from a sheet: refresh the tile summaries
    return;
  }
  if (!homeRoot?.isConnected) await paintHome();
  await paintSheet(wanted);
}

function renderIdentity(): void {
  signedIn.textContent = session.username ? t("header.signedInAs", { name: `${session.username}${session.is_owner ? ` (${t("header.owner")})` : ""}` }) : "";
  document.title = t("app.title", { name: deviceName });
}

/** Wires a static form: runs `submit`, shows its error inline. */
function wireForm(frag: DocumentFragment, formId: string, submit: (form: HTMLFormElement) => Promise<void>): void {
  const form = frag.getElementById(formId) as HTMLFormElement;
  const error = form.querySelector<HTMLElement>("#form-error") as HTMLElement;
  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    error.hidden = true;
    try {
      await submit(form);
    } catch (e) {
      error.textContent = errorMessage(e);
      error.hidden = false;
    }
  });
}

const field = (form: HTMLFormElement, id: string) => form.querySelector<HTMLInputElement>(`#${id}`) as HTMLInputElement;

function showLogin(): void {
  logout.hidden = true;
  const frag = fromTemplate("tpl-login");
  wireForm(frag, "login-form", async (form) => {
    await api("POST", "/api/login", { username: field(form, "username").value.trim(), password: field(form, "password").value });
    field(form, "password").value = "";
    await start();
  });
  show(frag);
}

const SETUP_ROLES = ["mom", "dad", "grandma", "grandpa", "guardian"];

/** First run: no administrator exists yet, so the person opening Milo creates the owner account. */
function showSetup(): void {
  logout.hidden = true;
  const frag = fromTemplate("tpl-setup");
  const language = frag.getElementById("setup-language") as HTMLSelectElement;
  for (const l of localeList()) language.append(h("option", { value: l.code, selected: l.code === getLocale() }, l.name));
  language.addEventListener("change", () => {
    // Repaint the whole form in the chosen language, keeping what was typed.
    const typed = ["setup-password", "setup-confirm"].map((id) => (document.getElementById(id) as HTMLInputElement).value);
    setLocale(language.value);
    showSetup();
    ["setup-password", "setup-confirm"].forEach((id, i) => ((document.getElementById(id) as HTMLInputElement).value = typed[i]));
    (document.getElementById("setup-language") as HTMLSelectElement).focus();
  });
  // "Who are you?" fills the username; "other" leaves it for the person to type.
  const who = frag.getElementById("setup-who") as HTMLSelectElement;
  const username = frag.getElementById("setup-username") as HTMLInputElement;
  const previous = document.getElementById("setup-who") as HTMLSelectElement | null;
  for (const r of SETUP_ROLES) who.append(h("option", { value: r, selected: r === (previous?.value ?? "mom") }, t(`setup.who.${r}`)));
  who.append(h("option", { value: "", selected: previous?.value === "" }, t("setup.who.other")));
  const fillUsername = () => {
    username.value = who.value ? t(`setup.who.${who.value}.user`) : "";
  };
  who.addEventListener("change", () => {
    fillUsername();
    if (!who.value) username.focus();
  });
  fillUsername();
  wireForm(frag, "setup-form", async (form) => {
    const password = field(form, "setup-password").value;
    if (password !== field(form, "setup-confirm").value) throw new Error(t("setup.mismatch"));
    await api("POST", "/api/setup", { username: field(form, "setup-username").value.trim(), password });
    // The language chosen on this screen becomes the device's interface and notification language.
    try {
      await api("PUT", "/api/settings/language", { interface_language: language.value, notification_language: language.value });
    } catch {
      /* the owner can change it later in System */
    }
    showSetupDone(field(form, "setup-username").value.trim());
  });
  show(frag);
}

/** Tells the new owner which username to sign in with next time, before entering the panel. */
function showSetupDone(username: string): void {
  show(h("section", { class: "card narrow" },
    h("h2", {}, t("setup.doneTitle")),
    h("p", {}, t("setup.doneBody")),
    h("p", { class: "username-callout" }, username),
    h("button", { type: "button", onclick: () => void start() }, t("setup.doneContinue"))));
}

/** Shown right after signing in with a generated password: nothing else works until it is changed. */
function showForcedChange(username: string): void {
  logout.hidden = false;
  const frag = fromTemplate("tpl-forced");
  (frag.getElementById("forced-title") as HTMLElement).textContent = t("forced.title", { username });
  wireForm(frag, "forced-form", async (form) => {
    await api("POST", "/api/account/password", { current_password: field(form, "current").value, new_password: field(form, "next").value });
    await start();
  });
  show(frag);
}

/** Interface language: the public endpoint before sign-in, the saved setting after. */
async function bootLocale(signedInNow: boolean): Promise<void> {
  if (!signedInNow) setLocale(storedLocale());
  try {
    if (signedInNow) {
      const s = await api<{ interface_language?: string }>("GET", "/api/settings/language");
      if (s.interface_language) setLocale(s.interface_language);
    } else {
      const s = await api<{ locale?: string; interface_language?: string }>("GET", "/api/locale");
      setLocale(s.locale ?? s.interface_language ?? getLocale());
    }
  } catch {
    /* older device or unreachable: keep the stored language, else English */
  }
  applyI18n();
}

export async function start(): Promise<void> {
  hideSheet();
  try {
    await bootLocale(false);
    const info = await api<SessionInfo>("GET", "/api/session");
    session = info;
    if (!info.authenticated) return info.setup_required ? showSetup() : showLogin();
    await bootLocale(true);
    if (info.must_change_password) return showForcedChange(info.username ?? "");
    const [status, sys] = await Promise.all([api<Status>("GET", "/api/status"), api<{ device_name: string }>("GET", "/api/system")]);
    isDev = status.hardware === "mock";
    deviceName = sys.device_name;
    const title = document.getElementById("device-name");
    if (title) title.textContent = deviceName;
    logout.hidden = false;
    renderIdentity();
    await route();
  } catch {
    show(fromTemplate("tpl-unreachable"));
  }
}

logout.addEventListener("click", async () => {
  await api("POST", "/api/logout");
  signedIn.textContent = "";
  active?.stop?.();
  await start();
});
window.addEventListener("hashchange", () => void route());
initSheet(dismissSheet);
// The interface language changed (System view): repaint the shell and the current view.
document.addEventListener("milo:locale", () => {
  renderIdentity();
  if (session.authenticated && !session.must_change_password) {
    if (isSheetOpen()) void paintHome();
    void route();
  }
});
