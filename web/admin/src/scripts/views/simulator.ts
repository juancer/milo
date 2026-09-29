import { api } from "../api";
import { clear, errorMessage, h, toast } from "../dom";

interface Button {
  id: number;
  name: string;
  color: string;
  enabled: boolean;
  actions: Record<string, { action: string }>;
}
interface Played {
  at: string;
  kind: string;
  label: string;
  duration_ms: number;
}

let timer: number | undefined;

export function stop(): void {
  if (timer !== undefined) clearInterval(timer);
  timer = undefined;
}

/** Virtual hardware for development: buttons, a microphone and a speaker log. */
export async function render(root: HTMLElement): Promise<void> {
  const buttons = await api<Button[]>("GET", "/api/buttons");
  clear(root);

  const say = h("input", { type: "text", placeholder: "What the child says next, e.g. Set a timer for one minute", maxLength: 300, ariaLabel: "What the child says" });
  const log = h("ul", { class: "list" });
  const volume = h("span", { class: "muted" });

  const press = async (b: Button, holdMs: number, times = 1) => {
    try {
      for (let i = 0; i < times; i++) {
        await api("POST", `/api/sim/buttons/${b.id}/press`, { hold_ms: holdMs });
        if (times > 1) await new Promise((r) => setTimeout(r, holdMs + 120));
      }
    } catch (e) {
      toast(errorMessage(e), true);
    }
  };

  root.append(
    h("section", { class: "card" },
      h("h2", {}, "Virtual device"),
      h("p", { class: "muted" }, "Development mode: no real hardware is used. Press the buttons like a child would."),
      h("div", { class: "pads" },
        ...buttons.filter((b) => b.enabled).map((b) =>
          h("div", { class: `pad c-${b.color}` },
            h("strong", {}, b.name),
            h("span", { class: "muted" }, Object.values(b.actions).map((a) => a.action).join(" / ") || "no action"),
            h("div", { class: "actions" },
              h("button", { type: "button", onclick: () => press(b, 60) }, "Press"),
              h("button", { type: "button", class: "secondary", onclick: () => press(b, 1200) }, "Hold"),
              h("button", { type: "button", class: "secondary", onclick: () => press(b, 50, 2) }, "Double"),
            )))),
    ),
    h("section", { class: "card" },
      h("h2", {}, "Microphone"),
      h("form", {
        class: "inline",
        onsubmit: async (ev: Event) => {
          ev.preventDefault();
          try {
            await api("POST", "/api/sim/utterance", { text: say.value });
            toast(say.value ? "Ready: press the talk / GPT button now" : "Cleared");
          } catch (e) {
            toast(errorMessage(e), true);
          }
        },
      }, say, h("button", { type: "submit" }, "Arm microphone")),
      h("p", { class: "muted" }, "The next recording will contain this sentence."),
    ),
    h("section", { class: "card" }, h("h2", {}, "Speaker"), volume, log),
  );

  const refresh = async () => {
    try {
      const data = await api<{ played: Played[]; volume: number }>("GET", "/api/sim/audio");
      volume.textContent = `Volume ${data.volume}%`;
      clear(log);
      if (data.played.length === 0) log.append(h("li", { class: "muted" }, "Nothing played yet."));
      for (const p of data.played.slice(0, 30)) {
        log.append(h("li", { class: "row" },
          h("span", {}, p.kind === "notification" ? `♪ ${p.label}` : `🔊 ${p.label}`),
          h("span", { class: "muted" }, `${new Date(p.at).toLocaleTimeString()} · ${p.duration_ms} ms`)));
      }
    } catch {
      /* transient */
    }
  };
  await refresh();
  stop();
  timer = window.setInterval(refresh, 1000);
}
