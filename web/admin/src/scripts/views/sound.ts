import { actions, card, checkbox, formCard, note, submit, toggle } from "../../components/ui";
import { t } from "../../i18n";
import { api, type Status } from "../api";
import { clear, h } from "../dom";
import { guard } from "../ui";

interface Notif {
  new_message_sound_enabled: boolean;
  emergency_sound_enabled: boolean;
  notify_pending_after_schedule: boolean;
  volume: number;
}

/** The same volume as a percentage and on the 0-10 scale people say aloud ("volume 7"). */
function volumeText(percent: number): string {
  return `${percent}% · ${Math.round(percent / 10)}/10`;
}

export async function render(root: HTMLElement): Promise<void> {
  const [cfg, status] = await Promise.all([api<Notif>("GET", "/api/settings/notifications"), api<Status>("GET", "/api/status")]);
  clear(root);

  const msg = checkbox(cfg.new_message_sound_enabled);
  const emg = checkbox(cfg.emergency_sound_enabled);
  const after = checkbox(cfg.notify_pending_after_schedule);
  const vol = h("input", { type: "range", min: "0", max: "100", value: String(cfg.volume), ariaLabel: t("sound.volume") });
  const volLabel = h("span", {}, volumeText(cfg.volume));
  vol.addEventListener("input", () => (volLabel.textContent = volumeText(Number(vol.value))));

  root.append(
    card(t("sound.audio"),
      h("p", {}, t("sound.backend", { backend: status.audio.backend, state: status.audio.available ? t("sound.available") : t("sound.unavailable"), volume: status.audio.volume })),
      status.audio.detail ? note(status.audio.detail) : null,
      note(t("sound.help"))),
    formCard(async (ev) => {
      ev.preventDefault();
      await guard(() => api("PUT", "/api/settings/notifications", {
        new_message_sound_enabled: msg.checked, emergency_sound_enabled: emg.checked,
        notify_pending_after_schedule: after.checked, volume: Number(vol.value),
      }), "common.saved");
    },
      h("h2", {}, t("sound.notifications")),
      note(t("sound.notificationsHelp")),
      toggle(t("sound.newMessage"), msg),
      toggle(t("sound.emergency"), emg),
      toggle(t("sound.afterHours"), after),
      h("label", { class: "field" }, h("span", {}, t("sound.volumeLabel"), " ", volLabel), vol),
      actions(submit(t("common.save")))),
  );
}
