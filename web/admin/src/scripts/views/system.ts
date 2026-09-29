import { api } from "../api";
import { clear, confirmTwice, errorMessage, field, h, toast } from "../dom";

interface System {
  device_name: string;
  timezone: string;
  message_retention_days: number;
  announce_sender: boolean;
  long_press_ms: number;
  double_press_ms: number;
  audio: { capture_device: string; playback_device: string; mixer_card: string; mixer_control: string };
}

export async function render(root: HTMLElement): Promise<void> {
  const [s, logs] = await Promise.all([api<System>("GET", "/api/system"), api<{ lines: string[] }>("GET", "/api/logs")]);
  clear(root);
  const f = {
    name: h("input", { type: "text", value: s.device_name, maxLength: 40, required: true, ariaLabel: "Device name" }),
    retention: h("input", { type: "number", value: String(s.message_retention_days), min: "0", max: "365", ariaLabel: "Message retention days" }),
    announce: h("input", { type: "checkbox", checked: s.announce_sender }),
    long: h("input", { type: "number", value: String(s.long_press_ms), min: "300", max: "3000", ariaLabel: "Long press ms" }),
    dbl: h("input", { type: "number", value: String(s.double_press_ms), min: "150", max: "1000", ariaLabel: "Double press ms" }),
    cap: h("input", { type: "text", value: s.audio.capture_device, ariaLabel: "Capture device" }),
    play: h("input", { type: "text", value: s.audio.playback_device, ariaLabel: "Playback device" }),
    card: h("input", { type: "text", value: s.audio.mixer_card, ariaLabel: "Mixer card" }),
    ctl: h("input", { type: "text", value: s.audio.mixer_control, ariaLabel: "Mixer control" }),
  };
  root.append(h("form", {
    class: "card",
    onsubmit: async (ev: Event) => {
      ev.preventDefault();
      try {
        await api("PUT", "/api/system", {
          device_name: f.name.value.trim(), timezone: s.timezone, message_retention_days: Number(f.retention.value),
          announce_sender: f.announce.checked, long_press_ms: Number(f.long.value), double_press_ms: Number(f.dbl.value),
          audio: { capture_device: f.cap.value.trim(), playback_device: f.play.value.trim(), mixer_card: f.card.value.trim(), mixer_control: f.ctl.value.trim() },
        });
        toast("Saved. Restart Nexo to apply the button and audio settings.");
      } catch (e) {
        toast(errorMessage(e), true);
      }
    },
  },
    h("h2", {}, "Device"),
    h("div", { class: "fields" }, field("Name", f.name), field("Timezone (change it in Schedules)", h("input", { type: "text", value: s.timezone, disabled: true }))),
    h("h2", {}, "Messages"),
    h("div", { class: "fields" }, field("Delete finished messages after (days, 0 = keep)", f.retention),
      h("label", { class: "check" }, f.announce, " Say who a message is from before playing it")),
    h("h2", {}, "Buttons"),
    h("div", { class: "fields" }, field("Long press starts after (ms)", f.long), field("Double press window (ms)", f.dbl)),
    h("h2", {}, "Audio devices"),
    h("p", { class: "muted" }, "The defaults suit the Waveshare WM8960 HAT. List sound cards on the device with: aplay -l"),
    h("div", { class: "fields" }, field("Microphone", f.cap), field("Speaker", f.play), field("Mixer card", f.card), field("Volume control", f.ctl)),
    h("div", { class: "actions" }, h("button", { type: "submit" }, "Save"))));

  root.append(h("section", { class: "card" }, h("h2", {}, "Restart"),
    h("p", { class: "muted" }, "Restarts the Nexo program (not the whole Raspberry Pi). It is back within a few seconds."),
    h("div", { class: "actions" }, h("button", {
      type: "button", class: "secondary",
      onclick: async (ev: Event) => {
        if (!confirmTwice(ev.currentTarget as HTMLButtonElement, "Restart Nexo")) return;
        try {
          await api("POST", "/api/system/restart");
          toast("Restarting… reload this page in a few seconds.");
        } catch (e) {
          toast(errorMessage(e), true);
        }
      },
    }, "Restart Nexo"))));

  root.append(h("section", { class: "card" }, h("h2", {}, "Recent log"),
    h("p", { class: "muted" }, "The latest events since Nexo started. Secrets and message contents never appear here."),
    h("pre", { class: "log" }, logs.lines.length ? logs.lines.join("\n") : "(empty)"),
    h("div", { class: "actions" }, h("button", { type: "button", class: "secondary", onclick: () => render(root) }, "Refresh"))));
}
