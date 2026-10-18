import { actions, badge, button, card, emptyState, list, note, row } from "../../components/ui";
import { t } from "../../i18n";
import { api } from "../api";
import { clear, errorMessage, h, when } from "../dom";
import { confirmTwice, guard, toast } from "../ui";

interface Skills {
  timers: { id: number; label: string; seconds_left: number }[];
  reminders: { id: number; text: string; due_at: string; source: string }[];
  music: { dir: string; tracks: string[]; count: number; state: string; title?: string };
  weather: { place: string; units: string; configured: boolean };
  calendar_configured: boolean;
  timezone: string;
}

let timer: number | undefined;

export function stop(): void {
  if (timer !== undefined) clearInterval(timer);
  timer = undefined;
}

function left(seconds: number): string {
  const m = Math.floor(seconds / 60);
  return m > 0 ? t("skills.minSec", { m, s: seconds % 60 }) : t("skills.sec", { s: seconds });
}

export async function render(root: HTMLElement): Promise<void> {
  const s = await api<Skills>("GET", "/api/skills");
  clear(root);
  const refresh = () => render(root);
  const run = async (fn: () => Promise<unknown>, okKey?: string) => {
    if (await guard(fn, okKey)) await refresh();
  };
  const tryIt = (path: string) => async () => {
    try {
      toast((await api<{ message: string }>("POST", path)).message);
    } catch (e) {
      toast(errorMessage(e), true);
    }
  };

  // ---- timers and reminders
  const text = h("input", { type: "text", maxLength: 200, placeholder: t("skills.reminderExample"), ariaLabel: t("skills.reminderText") });
  const due = h("input", { type: "datetime-local", ariaLabel: t("skills.when") });
  root.append(card(t("skills.timersAndReminders"),
    note(t("skills.timersHelp")),
    h("h3", {}, t("skills.runningTimers")),
    s.timers.length === 0 ? emptyState(t("common.none")) : null,
    list(...s.timers.map((tm) => row(h("span", {}, t("skills.timerLeft", { label: tm.label || t("skills.timer"), left: left(tm.seconds_left) })),
      button(t("common.cancel"), { variant: "secondary", onclick: () => run(() => api("DELETE", `/api/timers/${tm.id}`)) })))),
    h("h3", {}, t("skills.reminders")),
    s.reminders.length === 0 ? emptyState(t("common.none")) : null,
    list(...s.reminders.map((r) => row(h("span", {}, r.text, " ", badge(r.source)), h("span", { class: "muted" }, when(r.due_at)),
      button(t("common.cancel"), { variant: "secondary", onclick: () => run(() => api("DELETE", `/api/reminders/${r.id}`)) })))),
    h("form", { class: "inline", onsubmit: (ev: Event) => { ev.preventDefault(); void run(() => api("POST", "/api/reminders", { text: text.value.trim(), due: due.value }), "skills.reminderAdded"); } },
      text, due, h("button", { type: "submit" }, t("skills.addReminder"))),
    note(t("skills.timezoneNote", { timezone: s.timezone }))));

  // ---- music
  const dir = h("input", { type: "text", value: s.music.dir, ariaLabel: t("skills.musicFolder") });
  const query = h("input", { type: "text", placeholder: t("skills.songHint"), maxLength: 100, ariaLabel: t("skills.songToPlay") });
  const musicState = s.music.state === "playing" ? badge(t("skills.playing", { title: s.music.title ?? "" })) : s.music.state === "paused" ? badge(t("skills.paused", { title: s.music.title ?? "" }), true) : badge(t("skills.stopped"));
  root.append(card(t("skills.music"),
    note(t("skills.musicHelp")),
    h("p", {}, musicState, ` ${t("skills.songsFound", { count: s.music.count })}`),
    h("form", { class: "inline", onsubmit: (ev: Event) => { ev.preventDefault(); void run(() => api("PUT", "/api/music", { dir: dir.value.trim() }), "skills.folderSaved"); } },
      dir, h("button", { type: "submit" }, t("skills.useFolder")), button(t("skills.rescan"), { variant: "secondary", onclick: () => run(() => api("POST", "/api/music/rescan"), "skills.rescanned") })),
    h("form", { class: "inline", onsubmit: (ev: Event) => { ev.preventDefault(); void run(() => api("POST", "/api/music/play", { query: query.value.trim() })); } },
      query, h("button", { type: "submit" }, t("skills.play")), button(t("skills.stop"), { variant: "secondary", onclick: () => run(() => api("POST", "/api/music/stop")) })),
    h("details", {}, h("summary", {}, t("skills.songs")), list(...s.music.tracks.map((tr) => row(tr))))));

  // ---- weather
  // Suggestions: nearby towns (by internet address) first, anywhere else after a short wait.
  // A suggestion that is picked is saved with its coordinates, so a shared name is not guessed.
  interface PlaceOption { label: string; name: string; latitude: number; longitude: number }
  const options = h("datalist", { id: "weather-places" });
  let suggestions: PlaceOption[] = [];
  let typing: number | undefined;
  let searchSeq = 0;
  const place = h("input", {
    type: "text", maxLength: 80, autocomplete: "off", list: "weather-places",
    placeholder: s.weather.place || t("skills.yourTown"), ariaLabel: t("skills.town"),
    oninput: () => {
      window.clearTimeout(typing);
      const text = place.value.trim();
      if (text.length < 2) return;
      typing = window.setTimeout(async () => {
        const seq = ++searchSeq;
        try {
          const found = await api<PlaceOption[]>("GET", `/api/weather/places?q=${encodeURIComponent(text)}`);
          if (seq !== searchSeq) return; // a newer keystroke already asked again
          suggestions = found;
          options.replaceChildren(...found.map((p) => h("option", { value: p.label })));
        } catch { /* the field still works by typing the town and saving */ }
      }, 250);
    },
  });
  const chosenPlace = () => {
    const p = suggestions.find((o) => o.label === place.value.trim());
    return p ? { label: p.label, latitude: p.latitude, longitude: p.longitude } : { place: place.value.trim() };
  };
  const units = h("select", { ariaLabel: t("skills.units") },
    h("option", { value: "celsius", selected: s.weather.units === "celsius" }, t("skills.celsius")),
    h("option", { value: "fahrenheit", selected: s.weather.units === "fahrenheit" }, t("skills.fahrenheit")));
  root.append(card(t("skills.weather"),
    note(t("skills.weatherHelp")),
    h("p", {}, s.weather.configured ? badge(s.weather.place) : badge(t("skills.noTown"), true)),
    h("form", { class: "inline", onsubmit: (ev: Event) => { ev.preventDefault(); void run(() => api("PUT", "/api/weather", { ...chosenPlace(), units: units.value }), "common.saved"); } },
      place, options, units, h("button", { type: "submit" }, t("common.save"))),
    actions(
      button(t("skills.detect"), { onclick: () => run(() => api("POST", "/api/weather/detect"), "skills.detected") }),
      button(t("skills.tryIt"), { variant: "secondary", onclick: tryIt("/api/weather/test") })),
    note(t("skills.detectPrivacy")),
    note(t("skills.placeSearchPrivacy"))));

  // ---- calendar
  const url = h("input", { type: "password", autocomplete: "off", placeholder: s.calendar_configured ? t("skills.calendarSaved") : "https://…/basic.ics", ariaLabel: t("skills.calendarAddress") });
  root.append(card(t("skills.calendar"),
    note(t("skills.calendarHelp")),
    h("p", {}, s.calendar_configured ? badge(t("skills.addressSaved")) : badge(t("skills.noAddress"), true)),
    h("form", { class: "inline", onsubmit: (ev: Event) => { ev.preventDefault(); void run(() => api("PUT", "/api/calendar", { url: url.value.trim() }), "common.saved"); } },
      url, h("button", { type: "submit" }, t("skills.saveAddress")),
      s.calendar_configured ? button(t("common.remove"), { variant: "danger", onclick: async (ev) => {
        if (!confirmTwice(ev.currentTarget as HTMLButtonElement)) return;
        await run(() => api("DELETE", "/api/calendar"));
      } }) : null),
    actions(button(t("skills.tryWeek"), { variant: "secondary", onclick: tryIt("/api/calendar/test") }))));

  // Keep the running timers and music state fresh.
  stop();
  timer = window.setInterval(async () => {
    if (document.activeElement instanceof HTMLInputElement) return; // do not disturb typing
    await refresh();
  }, 5000);
}
