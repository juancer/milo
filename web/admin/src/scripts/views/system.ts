import { actions, button, card, field, fields, formCard, checkbox, note, select, submit, toggle } from "../../components/ui";
import { t } from "../../i18n";
import { api } from "../api";
import { clear, h } from "../dom";
import { loadLocales } from "../locales";
import { changeLocale, confirmTwice, guard, toast } from "../ui";

interface System {
  device_name: string;
  timezone: string;
  message_retention_days: number;
  announce_sender: boolean;
  long_press_ms: number;
  double_press_ms: number;
  audio: { capture_device: string; playback_device: string; mixer_card: string; mixer_control: string };
}
interface Language {
  interface_language: string;
  notification_language: string;
}

export async function render(root: HTMLElement): Promise<void> {
  const [s, logs, lang, locales] = await Promise.all([
    api<System>("GET", "/api/system"),
    api<{ lines: string[] }>("GET", "/api/logs"),
    api<Language>("GET", "/api/settings/language"),
    loadLocales(),
  ]);
  clear(root);

  // ---- languages: separate settings (the assistant and voice languages are in the Assistant tab)
  const options = locales.map((l): [string, string] => [l.code, l.name]);
  const iface = select(t("system.interfaceLanguage"), options, lang.interface_language);
  const notify = select(t("system.notificationLanguage"), options, lang.notification_language);
  root.append(formCard(async (ev) => {
    ev.preventDefault();
    if (await guard(() => api("PUT", "/api/settings/language", { interface_language: iface.value, notification_language: notify.value }))) {
      changeLocale(iface.value); // repaints the whole UI, including this view
      toast(t("common.saved"));
    }
  },
    h("h2", {}, t("system.languages")),
    note(t("system.languagesHelp")),
    fields(field(t("system.interfaceLanguage"), iface), field(t("system.notificationLanguage"), notify), submit(t("common.save")))));

  const f = {
    name: h("input", { type: "text", value: s.device_name, maxLength: 40, required: true, ariaLabel: t("system.deviceName") }),
    retention: h("input", { type: "number", value: String(s.message_retention_days), min: "0", max: "365", ariaLabel: t("system.retentionDays") }),
    announce: checkbox(s.announce_sender),
    long: h("input", { type: "number", value: String(s.long_press_ms), min: "300", max: "3000", ariaLabel: t("system.longPress") }),
    dbl: h("input", { type: "number", value: String(s.double_press_ms), min: "150", max: "1000", ariaLabel: t("system.doublePress") }),
    cap: h("input", { type: "text", value: s.audio.capture_device, ariaLabel: t("system.captureDevice") }),
    play: h("input", { type: "text", value: s.audio.playback_device, ariaLabel: t("system.playbackDevice") }),
    card: h("input", { type: "text", value: s.audio.mixer_card, ariaLabel: t("system.mixerCard") }),
    ctl: h("input", { type: "text", value: s.audio.mixer_control, ariaLabel: t("system.mixerControl") }),
  };
  root.append(formCard(async (ev) => {
    ev.preventDefault();
    await guard(() => api("PUT", "/api/system", {
      device_name: f.name.value.trim(), timezone: s.timezone, message_retention_days: Number(f.retention.value),
      announce_sender: f.announce.checked, long_press_ms: Number(f.long.value), double_press_ms: Number(f.dbl.value),
      audio: { capture_device: f.cap.value.trim(), playback_device: f.play.value.trim(), mixer_card: f.card.value.trim(), mixer_control: f.ctl.value.trim() },
    }), "system.saved");
  },
    h("h2", {}, t("system.device")),
    fields(field(t("common.name"), f.name), field(t("system.timezoneField"), h("input", { type: "text", value: s.timezone, disabled: true }))),
    h("h2", {}, t("system.messages")),
    fields(field(t("system.retentionLabel"), f.retention), toggle(t("system.announceSender"), f.announce)),
    h("h2", {}, t("system.buttons")),
    fields(field(t("system.longLabel"), f.long), field(t("system.doubleLabel"), f.dbl)),
    h("h2", {}, t("system.audioDevices")),
    note(t("system.audioHelp")),
    fields(field(t("system.microphone"), f.cap), field(t("system.speaker"), f.play), field(t("system.mixerCard"), f.card), field(t("system.volumeControl"), f.ctl)),
    actions(submit(t("common.save")))));

  root.append(card(t("system.restart"),
    note(t("system.restartHelp")),
    actions(button(t("system.restartButton"), {
      variant: "secondary",
      onclick: async (ev) => {
        if (!confirmTwice(ev.currentTarget as HTMLButtonElement)) return;
        await guard(() => api("POST", "/api/system/restart"), "system.restarting");
      },
    }))));

  root.append(card(t("system.log"),
    note(t("system.logHelp")),
    h("pre", { class: "log" }, logs.lines.length ? logs.lines.join("\n") : t("system.logEmpty")),
    actions(button(t("common.refresh"), { variant: "secondary", onclick: () => render(root) }))));
}
