import { api } from "../api";
import { badge, clear, confirmTwice, errorMessage, h, toast, when } from "../dom";

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
  return m > 0 ? `${m} min ${seconds % 60} s` : `${seconds} s`;
}

export async function render(root: HTMLElement): Promise<void> {
  const s = await api<Skills>("GET", "/api/skills");
  clear(root);
  const refresh = () => render(root);
  const run = async (fn: () => Promise<unknown>, ok?: string) => {
    try {
      await fn();
      if (ok) toast(ok);
      await refresh();
    } catch (e) {
      toast(errorMessage(e), true);
    }
  };
  const note = (text: string) => h("p", { class: "muted" }, text);

  // ---- timers and reminders
  const text = h("input", { type: "text", maxLength: 200, placeholder: "e.g. Grandma is visiting", ariaLabel: "Reminder text" });
  const due = h("input", { type: "datetime-local", ariaLabel: "When" });
  root.append(h("section", { class: "card" }, h("h2", {}, "Timers and reminders"),
    note("The child (or the assistant) sets timers and reminders by voice. Turn the “timer” and “reminders” features on in the Dashboard. You can add reminders here too."),
    h("h3", {}, "Running timers"),
    s.timers.length === 0 ? note("None.") : null,
    h("ul", { class: "list" }, ...s.timers.map((t) => h("li", { class: "row" }, h("span", {}, `${t.label || "Timer"} · ${left(t.seconds_left)} left`),
      h("button", { type: "button", class: "secondary", onclick: () => run(() => api("DELETE", `/api/timers/${t.id}`)) }, "Cancel")))),
    h("h3", {}, "Reminders"),
    s.reminders.length === 0 ? note("None.") : null,
    h("ul", { class: "list" }, ...s.reminders.map((r) => h("li", { class: "row" }, h("span", {}, r.text, " ", badge(r.source)), h("span", { class: "muted" }, when(r.due_at)),
      h("button", { type: "button", class: "secondary", onclick: () => run(() => api("DELETE", `/api/reminders/${r.id}`)) }, "Cancel")))),
    h("form", { class: "inline", onsubmit: (ev: Event) => { ev.preventDefault(); void run(() => api("POST", "/api/reminders", { text: text.value.trim(), due: due.value }), "Reminder added"); } },
      text, due, h("button", { type: "submit" }, "Add reminder")),
    note(`Times use the device timezone (${s.timezone}).`)));

  // ---- music
  const dir = h("input", { type: "text", value: s.music.dir, ariaLabel: "Music folder" });
  const query = h("input", { type: "text", placeholder: "song name (empty = surprise)", maxLength: 100, ariaLabel: "Song to play" });
  root.append(h("section", { class: "card" }, h("h2", {}, "Music"),
    note("Copy .mp3 or .wav files into the music folder (for example with a USB stick or scp). The child asks “play the Frozen song”; the closest name wins. Milo keeps playing through the folder until told to stop, and a notification or spoken answer interrupts it."),
    h("p", {}, s.music.state === "playing" ? badge(`playing: ${s.music.title ?? ""}`) : s.music.state === "paused" ? badge(`paused: ${s.music.title ?? ""}`, true) : badge("stopped"), ` ${s.music.count} songs found`),
    h("form", { class: "inline", onsubmit: (ev: Event) => { ev.preventDefault(); void run(() => api("PUT", "/api/music", { dir: dir.value.trim() }), "Folder saved"); } },
      dir, h("button", { type: "submit" }, "Use this folder"), h("button", { type: "button", class: "secondary", onclick: () => run(() => api("POST", "/api/music/rescan"), "Rescanned") }, "Rescan")),
    h("form", { class: "inline", onsubmit: (ev: Event) => { ev.preventDefault(); void run(() => api("POST", "/api/music/play", { query: query.value.trim() })); } },
      query, h("button", { type: "submit" }, "Play"), h("button", { type: "button", class: "secondary", onclick: () => run(() => api("POST", "/api/music/stop")) }, "Stop")),
    h("details", {}, h("summary", {}, "Songs found"), h("ul", { class: "list" }, ...s.music.tracks.map((t) => h("li", { class: "row" }, t))))));

  // ---- weather
  const place = h("input", { type: "text", maxLength: 80, placeholder: s.weather.place || "your town", ariaLabel: "Town" });
  const units = h("select", { ariaLabel: "Units" },
    h("option", { value: "celsius", selected: s.weather.units === "celsius" }, "Celsius"),
    h("option", { value: "fahrenheit", selected: s.weather.units === "fahrenheit" }, "Fahrenheit"));
  root.append(h("section", { class: "card" }, h("h2", {}, "Weather"),
    note("Uses the free Open-Meteo service. Only the coordinates of your town are sent, nothing else. Turn the “weather” feature on in the Dashboard."),
    h("p", {}, s.weather.configured ? badge(s.weather.place) : badge("no town set", true)),
    h("form", { class: "inline", onsubmit: (ev: Event) => { ev.preventDefault(); void run(() => api("PUT", "/api/weather", { place: place.value.trim(), units: units.value }), "Saved"); } },
      place, units, h("button", { type: "submit" }, "Save")),
    h("div", { class: "actions" }, h("button", { type: "button", class: "secondary", onclick: async () => {
      try { toast((await api<{ message: string }>("POST", "/api/weather/test")).message); } catch (e) { toast(errorMessage(e), true); }
    } }, "Try it"))));

  // ---- calendar
  const url = h("input", { type: "password", autocomplete: "off", placeholder: s.calendar_configured ? "(saved - paste to replace)" : "https://…/basic.ics", ariaLabel: "Calendar address" });
  root.append(h("section", { class: "card" }, h("h2", {}, "Calendar"),
    note("Read-only. Paste your calendar's secret iCalendar address (in Google Calendar: Settings → your calendar → “Secret address in iCal format”). It works like a password, so it is stored encrypted and never shown again. Repeating events are supported; unusual repeat rules may be missed. Event titles are read aloud and, when the assistant reads them, sent to the AI service."),
    h("p", {}, s.calendar_configured ? badge("address saved") : badge("no address", true)),
    h("form", { class: "inline", onsubmit: (ev: Event) => { ev.preventDefault(); void run(() => api("PUT", "/api/calendar", { url: url.value.trim() }), "Saved"); } },
      url, h("button", { type: "submit" }, "Save address"),
      s.calendar_configured ? h("button", { type: "button", class: "danger", onclick: async (ev: Event) => {
        if (!confirmTwice(ev.currentTarget as HTMLButtonElement, "Remove")) return;
        await run(() => api("DELETE", "/api/calendar"));
      } }, "Remove") : null),
    h("div", { class: "actions" }, h("button", { type: "button", class: "secondary", onclick: async () => {
      try { toast((await api<{ message: string }>("POST", "/api/calendar/test")).message); } catch (e) { toast(errorMessage(e), true); }
    } }, "Try it (this week)"))));

  // Keep the running timers and music state fresh.
  stop();
  timer = window.setInterval(async () => {
    if (document.activeElement instanceof HTMLInputElement) return; // do not disturb typing
    await refresh();
  }, 5000);
}
