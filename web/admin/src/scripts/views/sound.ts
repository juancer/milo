import { api, type Status } from "../api";
import { clear, errorMessage, h, toast } from "../dom";

interface Notif {
  new_message_sound_enabled: boolean;
  emergency_sound_enabled: boolean;
  notify_pending_after_schedule: boolean;
  volume: number;
}

export async function render(root: HTMLElement): Promise<void> {
  const [cfg, status] = await Promise.all([api<Notif>("GET", "/api/settings/notifications"), api<Status>("GET", "/api/status")]);
  clear(root);

  const msg = h("input", { type: "checkbox", checked: cfg.new_message_sound_enabled });
  const emg = h("input", { type: "checkbox", checked: cfg.emergency_sound_enabled });
  const after = h("input", { type: "checkbox", checked: cfg.notify_pending_after_schedule });
  const vol = h("input", { type: "range", min: "0", max: "100", value: String(cfg.volume), ariaLabel: "Notification volume" });
  const volLabel = h("span", {}, `${cfg.volume}%`);
  vol.addEventListener("input", () => (volLabel.textContent = `${vol.value}%`));

  root.append(
    h("section", { class: "card" }, h("h2", {}, "Audio"),
      h("p", {}, `Backend: ${status.audio.backend} · ${status.audio.available ? "available" : "unavailable"} · speaker volume ${status.audio.volume}%`),
      status.audio.detail ? h("p", { class: "muted" }, status.audio.detail) : null,
      h("p", { class: "muted" }, "Speaker volume is changed with the volume buttons (set_volume action) or by voice. Test the hardware on the device with: nexo test-audio")),
    h("form", {
      class: "card",
      onsubmit: async (ev: Event) => {
        ev.preventDefault();
        try {
          await api("PUT", "/api/settings/notifications", {
            new_message_sound_enabled: msg.checked, emergency_sound_enabled: emg.checked,
            notify_pending_after_schedule: after.checked, volume: Number(vol.value),
          });
          toast("Saved");
        } catch (e) {
          toast(errorMessage(e), true);
        }
      },
    },
      h("h2", {}, "Notification sounds"),
      h("p", { class: "muted" }, "Sounds never reveal who wrote or what. A short “piii” means a message is waiting; three long tones mean an emergency."),
      h("label", { class: "check" }, msg, " Play a sound for new messages"),
      h("label", { class: "check" }, emg, " Play the emergency sound"),
      h("label", { class: "check" }, after, " Announce messages that arrived outside usage hours when hours begin"),
      h("label", { class: "field" }, h("span", {}, "Notification volume ", volLabel), vol),
      h("div", { class: "actions" }, h("button", { type: "submit" }, "Save"))),
  );
}
