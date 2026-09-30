import { button, card, note, row } from "../../components/ui";
import { t } from "../../i18n";
import { api } from "../api";
import { clear, h, timeOf } from "../dom";
import { guard } from "../ui";

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

  const say = h("input", { type: "text", placeholder: t("simulator.sayHint"), maxLength: 300, ariaLabel: t("simulator.says") });
  const log = h("ul", { class: "list" });
  const volume = h("span", { class: "muted" });

  const press = async (b: Button, holdMs: number, times = 1) => {
    await guard(async () => {
      for (let i = 0; i < times; i++) {
        await api("POST", `/api/sim/buttons/${b.id}/press`, { hold_ms: holdMs });
        if (times > 1) await new Promise((r) => setTimeout(r, holdMs + 120));
      }
    });
  };

  root.append(
    card(t("simulator.title"),
      note(t("simulator.help")),
      h("div", { class: "pads" },
        ...buttons.filter((b) => b.enabled).map((b) =>
          h("div", { class: `pad c-${b.color}` },
            h("strong", {}, b.name),
            h("span", { class: "muted" }, Object.values(b.actions).map((a) => a.action).join(" / ") || t("simulator.noAction")),
            h("div", { class: "actions" },
              button(t("simulator.press"), { onclick: () => press(b, 60) }),
              button(t("simulator.hold"), { variant: "secondary", onclick: () => press(b, 1200) }),
              button(t("simulator.double"), { variant: "secondary", onclick: () => press(b, 50, 2) }),
            )))),
    ),
    card(t("simulator.wakeWord"),
      note(t("simulator.wakeHelp")),
      button(t("simulator.sayWake"), { onclick: () => guard(() => api("POST", "/api/sim/wake")) })),
    card(t("simulator.microphone"),
      h("form", {
        class: "inline",
        onsubmit: async (ev: Event) => {
          ev.preventDefault();
          await guard(() => api("POST", "/api/sim/utterance", { text: say.value }), say.value ? "simulator.armed" : "simulator.cleared");
        },
      }, say, h("button", { type: "submit" }, t("simulator.arm"))),
      note(t("simulator.micHelp")),
    ),
    card(t("simulator.speaker"), volume, log),
  );

  const refresh = async () => {
    try {
      const data = await api<{ played: Played[]; volume: number }>("GET", "/api/sim/audio");
      volume.textContent = t("simulator.volume", { volume: data.volume });
      clear(log);
      if (data.played.length === 0) log.append(h("li", { class: "empty" }, t("simulator.nothingPlayed")));
      for (const p of data.played.slice(0, 30)) {
        log.append(row(
          h("span", {}, p.kind === "notification" ? `♪ ${p.label}` : p.kind === "music" ? `♫ ${p.label}` : `▶ ${p.label}`),
          h("span", { class: "muted" }, `${timeOf(p.at)} · ${p.duration_ms} ms`)));
      }
    } catch {
      /* transient */
    }
  };
  await refresh();
  stop();
  timer = window.setInterval(refresh, 1000);
}
