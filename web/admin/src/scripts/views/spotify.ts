import { actions, alert, badge, card, checkbox, field, fields, formCard, note, submit, toggle } from "../../components/ui";
import { t } from "../../i18n";
import { api } from "../api";
import { clear, h } from "../dom";
import { confirmDialog, guard } from "../ui";

interface Info {
  binary: string;
  binary_found: boolean;
  device_name: string;
  running: boolean;
  signed_in: boolean;
  username?: string;
  playing: boolean;
  track?: string;
  pairing?: { url: string; code: string; expires_at: string };
  voice_control: boolean;
  search_configured: boolean;
  error?: string;
}

let timer: number | undefined;

export function stop(): void {
  if (timer !== undefined) clearInterval(timer);
  timer = undefined;
}

/** Only ever link to Spotify's own pairing page, whatever the receiver reports. */
const PAIR_URL = "https://www.spotify.com/pair";
const pairLink = (url: string): string => (/^https:\/\/([a-z0-9-]+\.)?spotify\.com\//i.test(url) ? url : PAIR_URL);

export async function render(root: HTMLElement): Promise<void> {
  const info = await api<Info>("GET", "/api/spotify");
  clear(root);

  // ---- status (refreshed while the sheet is open, so the pairing code and "now playing" stay current)
  const status = h("div", { class: "spotify-status" });
  const paint = (i: Info) => {
    clear(status);
    if (!i.binary_found) {
      status.append(alert(t("spotify.notInstalled", { path: i.binary }), "info"), note(t("spotify.installSteps")));
      return;
    }
    if (!i.running) {
      status.append(note(t("spotify.starting")));
      return;
    }
    if (!i.signed_in) {
      const p = i.pairing;
      status.append(
        note(t("spotify.pairIntro")),
        p ? h("div", { class: "pairing" },
          h("p", { class: "muted" }, t("spotify.pairStep1"), " ", h("a", { href: pairLink(p.url), target: "_blank", rel: "noopener noreferrer" }, pairLink(p.url).replace(/^https:\/\//, ""))),
          h("p", { class: "muted" }, t("spotify.pairStep2")),
          h("p", { class: "pairing-code", "aria-live": "polite" }, p.code)) : note(t("spotify.pairWaiting")));
      return;
    }
    status.append(
      h("p", {}, badge(t("spotify.connected", { user: i.username ?? "" })), " ", i.playing ? badge(t("spotify.playing")) : null),
      ...(i.track ? [h("p", { class: "muted" }, t("spotify.nowPlaying", { track: i.track }))] : []),
      note(t("spotify.useIt", { name: i.device_name })),
      actions(h("button", {
        type: "button", class: "secondary",
        onclick: async () => {
          if (!(await confirmDialog(t("spotify.signOutConfirm"), t("spotify.signOut")))) return;
          if (await guard(() => api("POST", "/api/spotify/signout"), "spotify.signedOut")) await render(root);
        },
      }, t("spotify.signOut"))));
    if (i.error) status.append(h("p", { class: "muted" }, i.error));
  };
  paint(info);
  stop();
  timer = window.setInterval(() => {
    if (!status.isConnected) return stop();
    api<Info>("GET", "/api/spotify").then(paint).catch(() => undefined);
  }, 3000);

  root.append(card(t("spotify.title"), note(t("spotify.about")), note(t("spotify.unofficial")), status));

  // ---- settings
  const binary = h("input", { type: "text", value: info.binary, maxLength: 200, ariaLabel: t("spotify.binary") });
  const voice = checkbox(info.voice_control);
  root.append(formCard((ev) => {
    ev.preventDefault();
    void guard(() => api("PUT", "/api/spotify", { binary: binary.value.trim(), voice_control: voice.checked }), "common.saved").then((ok) => (ok ? render(root) : undefined));
  },
    h("h3", {}, t("spotify.settings")),
    fields(field(t("spotify.binary"), binary)),
    toggle(t("spotify.voiceControl"), voice),
    alert(t("spotify.voiceWarning"), "info"),
    actions(submit(t("common.save")))));

  // ---- artist search (optional): the credentials of a free Spotify developer app make "pon Camilo" reliable
  const clientId = h("input", { type: "text", autocomplete: "off", maxLength: 64, placeholder: info.search_configured ? t("spotify.searchSaved") : "", ariaLabel: t("spotify.clientId") });
  const clientSecret = h("input", { type: "password", autocomplete: "off", maxLength: 64, placeholder: info.search_configured ? t("spotify.searchSaved") : "", ariaLabel: t("spotify.clientSecret") });
  root.append(formCard((ev) => {
    ev.preventDefault();
    void guard(() => api("PUT", "/api/spotify/search", { client_id: clientId.value.trim(), client_secret: clientSecret.value.trim() }), "spotify.searchStored").then((ok) => (ok ? render(root) : undefined));
  },
    h("h3", {}, t("spotify.searchTitle")),
    h("p", {}, info.search_configured ? badge(t("spotify.searchOn")) : badge(t("spotify.searchOff"), true)),
    note(t("spotify.searchHelp")),
    h("ol", { class: "muted" },
      h("li", {}, t("spotify.searchStep1"), " ", h("a", { href: "https://developer.spotify.com/dashboard", target: "_blank", rel: "noopener noreferrer" }, "developer.spotify.com/dashboard")),
      h("li", {}, t("spotify.searchStep2")),
      h("li", {}, t("spotify.searchStep3"))),
    fields(field(t("spotify.clientId"), clientId), field(t("spotify.clientSecret"), clientSecret)),
    actions(submit(t("common.save")), ...(info.search_configured ? [h("button", {
      type: "button", class: "secondary",
      onclick: async () => {
        if (!(await confirmDialog(t("spotify.searchRemoveConfirm"), t("spotify.searchRemove")))) return;
        if (await guard(() => api("DELETE", "/api/spotify/search"), "spotify.searchRemoved")) await render(root);
      },
    }, t("spotify.searchRemove"))] : []))));
}
