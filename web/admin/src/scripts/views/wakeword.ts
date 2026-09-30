import { actions, badge, button, card, emptyState, field, fields, formCard, list, note, row, select, submit } from "../../components/ui";
import { t } from "../../i18n";
import { api } from "../api";
import { clear, h, timeOf, when } from "../dom";
import { confirmTwice, guard, toast } from "../ui";

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

  const engine = select(t("wakeword.engine"), [["template", t("wakeword.engineBuiltin")], ["external", t("wakeword.engineExternal")]], s.engine);
  const phrase = h("input", { type: "text", value: s.phrase, maxLength: 40, ariaLabel: t("wakeword.phrase") });
  const sens = h("input", { type: "range", min: "1", max: "10", value: String(s.sensitivity), ariaLabel: t("wakeword.sensitivity") });
  const sensLabel = h("span", {}, String(s.sensitivity));
  sens.addEventListener("input", () => (sensLabel.textContent = sens.value));
  const path = h("input", { type: "text", value: s.external_path, placeholder: "/opt/milo/wake-helper", ariaLabel: t("wakeword.helperPath") });

  root.append(card(t("wakeword.title"),
    h("p", {}, !st.feature.enabled ? badge(t("common.featureOff"), true) : st.feature.running ? badge(t("wakeword.listening")) : badge(t("common.notRunning"), true), " ",
      st.feature.enabled ? null : h("span", { class: "muted" }, t("wakeword.turnOn")),
      st.feature.last_error ? h("span", { class: "error" }, ` ${st.feature.last_error}`) : null),
    note(t("wakeword.intro")),
    st.simulated ? note(t("wakeword.simulated")) : null));

  root.append(formCard(async (ev) => {
    ev.preventDefault();
    await guard(async () => {
      await api("PUT", "/api/wakeword", { engine: engine.value, phrase: phrase.value.trim(), sensitivity: Number(sens.value), external_path: path.value.trim() });
      await refresh();
    }, "common.saved");
  },
    h("h2", {}, t("common.settings")),
    fields(field(t("wakeword.engine"), engine), field(t("wakeword.phraseLabel"), phrase), field(t("wakeword.helperLabel"), path)),
    h("label", { class: "field" }, h("span", {}, t("wakeword.sensitivityLabel"), " ", sensLabel, " - ", t("wakeword.sensitivityHelp")), sens),
    actions(submit(t("common.save")))));

  if (s.engine === "template") {
    const record = button(t("wakeword.record"), {
      onclick: async () => {
        record.disabled = true;
        toast(t("wakeword.sayNow", { phrase: s.phrase }));
        await guard(() => api("POST", "/api/wakeword/samples"), "wakeword.sampleSaved");
        await refresh();
      },
    });
    root.append(card(t("wakeword.samples"),
      note(t("wakeword.samplesHelp", { count: st.max_samples > 3 ? t("wakeword.threeToFive") : t("wakeword.aFew") })),
      list(...st.samples.map((sm) => row(
        h("span", {}, t("wakeword.sample", { id: sm.id, seconds: (sm.frames / 100).toFixed(1) })), h("span", { class: "muted" }, when(sm.created_at)),
        button(t("common.delete"), { variant: "secondary", onclick: async (ev) => {
          if (!confirmTwice(ev.currentTarget as HTMLButtonElement)) return;
          if (await guard(() => api("DELETE", `/api/wakeword/samples/${sm.id}`))) await refresh();
        } })))),
      st.samples.length === 0 ? emptyState(t("wakeword.noSamples")) : null,
      actions(record)));

    const scores = h("ul", { class: "list" });
    root.append(card(t("wakeword.live"),
      note(t("wakeword.liveHelp", { threshold: st.status.threshold.toFixed(2) })),
      scores));
    const paint = async () => {
      try {
        const fresh = await api<State>("GET", "/api/wakeword");
        clear(scores);
        if (fresh.status.scores.length === 0) scores.append(h("li", { class: "empty" }, t("wakeword.nothingHeard")));
        for (const sc of [...fresh.status.scores].reverse()) {
          const hit = sc.distance <= fresh.status.threshold;
          scores.append(row(h("span", {}, `${sc.distance.toFixed(2)} `, hit ? badge(t("wakeword.wouldWake")) : badge(t("wakeword.ignored"))),
            h("span", { class: "muted" }, timeOf(sc.at))));
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
