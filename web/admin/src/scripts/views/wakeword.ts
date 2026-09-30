import { api } from "../api";
import { badge, clear, confirmTwice, errorMessage, field, h, toast, when } from "../dom";

interface State {
  settings: { engine: string; phrase: string; sensitivity: number; external_path: string };
  feature: { enabled: boolean; running: boolean; last_error?: string };
  status: { running: boolean; engine: string; threshold: number; last_detection?: string; scores: { at: string; distance: number }[] };
  samples: { id: number; frames: number; created_at: string }[];
  max_samples: number;
  simulated: boolean;
}

let timer: number | undefined;

export function stop(): void {
  if (timer !== undefined) clearInterval(timer);
  timer = undefined;
}

export async function render(root: HTMLElement): Promise<void> {
  const st = await api<State>("GET", "/api/wakeword");
  clear(root);
  const refresh = () => render(root);
  const s = st.settings;

  const engine = h("select", { ariaLabel: "Engine" },
    h("option", { value: "template", selected: s.engine === "template" }, "Built-in (learns your voice)"),
    h("option", { value: "external", selected: s.engine === "external" }, "External helper (openWakeWord, Porcupine…)"));
  const phrase = h("input", { type: "text", value: s.phrase, maxLength: 40, ariaLabel: "Wake phrase" });
  const sens = h("input", { type: "range", min: "1", max: "10", value: String(s.sensitivity), ariaLabel: "Sensitivity" });
  const sensLabel = h("span", {}, String(s.sensitivity));
  sens.addEventListener("input", () => (sensLabel.textContent = sens.value));
  const path = h("input", { type: "text", value: s.external_path, placeholder: "/opt/milo/wake-helper", ariaLabel: "Helper path" });

  root.append(h("section", { class: "card" }, h("h2", {}, "Wake word"),
    h("p", {}, !st.feature.enabled ? badge("feature off", true) : st.feature.running ? badge("listening") : badge("not running", true), " ",
      st.feature.enabled ? null : h("span", { class: "muted" }, "Turn the “wakeword” feature on in the Dashboard."),
      st.feature.last_error ? h("span", { class: "error" }, ` ${st.feature.last_error}`) : null),
    h("p", { class: "muted" },
      "Say the phrase and the assistant starts listening, without touching a button. Detection happens on the device: no audio is sent anywhere until the phrase is recognised. The buttons keep working either way."),
    st.simulated ? h("p", { class: "muted" }, "Development mode: use “Say the wake word” in the Simulator tab.") : null));

  root.append(h("form", {
    class: "card",
    onsubmit: async (ev: Event) => {
      ev.preventDefault();
      try {
        await api("PUT", "/api/wakeword", { engine: engine.value, phrase: phrase.value.trim(), sensitivity: Number(sens.value), external_path: path.value.trim() });
        toast("Saved");
        await refresh();
      } catch (e) {
        toast(errorMessage(e), true);
      }
    },
  },
    h("h2", {}, "Settings"),
    h("div", { class: "fields" }, field("Engine", engine), field("Phrase (label)", phrase), field("Helper program (external engine)", path)),
    h("label", { class: "field" }, h("span", {}, "Sensitivity ", sensLabel, " - higher hears you more easily but may wake by mistake"), sens),
    h("div", { class: "actions" }, h("button", { type: "submit" }, "Save"))));

  if (s.engine === "template") {
    const record = h("button", {
      type: "button",
      onclick: async () => {
        record.disabled = true;
        toast(`Say “${s.phrase}” now, close to the device…`);
        try {
          await api("POST", "/api/wakeword/samples");
          toast("Sample saved");
        } catch (e) {
          toast(errorMessage(e), true);
        }
        await refresh();
      },
    }, "Record a sample");
    root.append(h("section", { class: "card" }, h("h2", {}, "Your voice samples"),
      h("p", { class: "muted" },
        `Record the phrase ${st.max_samples > 3 ? "three to five" : "a few"} times, the way it will be said in the room. More variety (near, far, quiet, loud) makes it more reliable. The built-in engine is experimental and learns the voices you record; for the best accuracy use an external neural helper.`),
      h("ul", { class: "list" }, ...st.samples.map((sm) => h("li", { class: "row" },
        h("span", {}, `Sample ${sm.id} · ${(sm.frames / 100).toFixed(1)} s`), h("span", { class: "muted" }, when(sm.created_at)),
        h("button", { type: "button", class: "secondary", onclick: async (ev: Event) => {
          if (!confirmTwice(ev.currentTarget as HTMLButtonElement, "Delete")) return;
          await api("DELETE", `/api/wakeword/samples/${sm.id}`);
          await refresh();
        } }, "Delete")))),
      st.samples.length === 0 ? h("p", { class: "muted" }, "No samples yet.") : null,
      h("div", { class: "actions" }, record)));

    const scores = h("ul", { class: "list" });
    root.append(h("section", { class: "card" }, h("h2", {}, "Live check"),
      h("p", { class: "muted" }, `Say things near the device. Each line is how different a sound was from your samples; it wakes when the number is at or below ${st.status.threshold.toFixed(2)}. Raise the sensitivity if your phrase scores above that, lower it if other words score below.`),
      scores));
    const paint = async () => {
      try {
        const fresh = await api<State>("GET", "/api/wakeword");
        clear(scores);
        if (fresh.status.scores.length === 0) scores.append(h("li", { class: "muted" }, "Nothing heard yet."));
        for (const sc of [...fresh.status.scores].reverse()) {
          const hit = sc.distance <= fresh.status.threshold;
          scores.append(h("li", { class: "row" }, h("span", {}, `${sc.distance.toFixed(2)} `, hit ? badge("would wake") : badge("ignored", false)),
            h("span", { class: "muted" }, new Date(sc.at).toLocaleTimeString())));
        }
      } catch {
        /* transient */
      }
    };
    await paint();
    stop();
    timer = window.setInterval(paint, 2000);
  }
}
