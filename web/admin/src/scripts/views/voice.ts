import { actions, badge, button, card, emptyState, field, fields, formCard, list, note, row, select, submit } from "../../components/ui";
import { getLocale, t } from "../../i18n";
import { api } from "../api";
import { clear, errorMessage, h } from "../dom";
import { AUTO, loadLocales } from "../locales";
import { guard, toast } from "../ui";
import { modelField, saveSettings, showOnly, type State } from "./assistantShared";

interface VoiceRow { key: string; name: string; quality: string; language: string; country: string; speakers: number; size_bytes: number; installed: boolean; fast: boolean }
interface VoiceJob { key: string; running: boolean; stage: string; done: number; total: number; error?: string }
interface VoiceList { language: string; languages: { code: string; name: string }[]; voices: VoiceRow[]; current: string; in_use: { language: string; key: string }[]; piper_installed: boolean; job: VoiceJob }
interface Similarity { source_f0: number; matches: { voice: VoiceRow; f0: number; diff_hz: number }[]; considered: number; analysed: number; capped: boolean; slower: number }

/** "davefx" from "es_ES-davefx-medium". */
const voiceName = (key: string): string => key.split("-").slice(1, -1).join("-") || key;
/** The most demos one suggestion downloads; the server enforces the same cap. */
const MAX_SUGGESTION_DEMOS = 12;

const mb = (bytes: number): string => (bytes / 1048576).toFixed(bytes < 10485760 ? 1 : 0);

/** Voice picker: the Piper catalog for a language, a demo of each voice, and download on choosing. */
function voicesCard(initialLanguage?: string): HTMLElement {
  const body = h("div", {});
  // One demo plays at a time. The same button plays and pauses it; starting another stops this one.
  let current: { key: string; audio: HTMLAudioElement; btn: HTMLButtonElement } | null = null;
  const setLabel = (btn: HTMLButtonElement, on: boolean) => { btn.textContent = on ? t("voices.pause") : t("voices.play"); };
  const stopDemo = () => { current?.audio.pause(); current = null; };
  const toggleDemo = (key: string, btn: HTMLButtonElement) => {
    if (current?.key === key) {
      if (current.audio.paused) void current.audio.play(); else current.audio.pause();
      return;
    }
    stopDemo();
    const audio = new Audio(`/api/voices/${encodeURIComponent(key)}/sample`);
    current = { key, audio, btn };
    audio.addEventListener("play", () => setLabel(btn, true));
    audio.addEventListener("pause", () => setLabel(btn, false));
    audio.addEventListener("ended", () => setLabel(btn, false));
    audio.play().catch(() => { toast(t("voices.demoFailed"), true); setLabel(btn, false); });
  };
  const load = async (language?: string): Promise<void> => {
    stopDemo();
    let data: VoiceList;
    try {
      data = await api<VoiceList>("GET", `/api/voices${language ? `?lang=${encodeURIComponent(language)}` : ""}`);
    } catch (e) {
      clear(body);
      body.append(h("p", { class: "error" }, e instanceof Error ? e.message : t("voices.catalogError")));
      return;
    }
    const busy = data.job.running;
    const progress = h("p", { class: "muted", role: "status" });
    const showProgress = (j: VoiceJob) => {
      const stage = j.stage === "piper" ? t("voices.downloadingPiper") : t("voices.downloadingVoice");
      progress.textContent = j.running ? `${stage} ${j.total > 0 ? t("voices.progress", { done: mb(j.done), total: mb(j.total) }) : mb(j.done) + " MB"}` : "";
    };
    showProgress(data.job);
    const langSelect = select(t("voices.language"), data.languages.map((l): [string, string] => [l.code, l.name]), data.language, { onchange: () => void load(langSelect.value) });
    const demoButton = (key: string): HTMLButtonElement => {
      const btn = button(t("voices.play"), { variant: "secondary" });
      btn.addEventListener("click", () => toggleDemo(key, btn));
      return btn;
    };
    const langName = (code: string): string => data.languages.find((l) => l.code === code)?.name.split(" (")[0] ?? code;
    const inUseKeys = new Set(data.in_use.map((u) => u.key));
    // Every row has the same three columns (what the voice is, Play/Pause, the action), so the
    // buttons line up whatever the length of a voice's name. `extra` is a short note after the size.
    const renderRow = (v: VoiceRow, extra?: string): HTMLElement => {
      const choice = inUseKeys.has(v.key) ? badge(t("voices.inUse"))
        : v.installed ? h("div", { class: "voice-action" },
          button(t("voices.use"), { onclick: async () => { if (await guard(() => api("POST", `/api/voices/${v.key}/use`), "voices.nowUsing")) await load(data.language); } }),
          button(t("voices.remove"), { variant: "danger", onclick: async () => { if (await guard(() => api("DELETE", `/api/voices/${v.key}`))) await load(data.language); } }))
          : h("div", { class: "voice-action" }, button(t("voices.download"), {
            disabled: busy,
            onclick: async () => {
              if (await guard(() => api("POST", `/api/voices/${v.key}/install`, { use: true }))) await load(data.language);
            },
          }));
      return h("li", { class: "row voice-row" },
        h("div", { class: "voice-info" },
          h("strong", {}, v.name), h("span", { class: "muted" }, ` · ${v.country}`),
          h("div", { class: "voice-meta" }, badge(v.quality), " ", v.fast ? badge(t("voices.fast")) : badge(t("voices.slow"), true),
            h("span", { class: "muted" }, ` ${mb(v.size_bytes)} MB${v.speakers > 1 ? " · " + t("voices.speakers", { n: v.speakers }) : ""}${extra ? " · " + extra : ""}`))),
        h("div", { class: "voice-play" }, demoButton(v.key)),
        h("div", { class: "voice-choice" }, choice));
    };
    // Fast voices only. Medium ones (slow on a Raspberry Pi 3) appear only for a language that has no
    // fast voice at all; installed and in-use voices always stay.
    const fast = data.voices.filter((v) => v.fast);
    const offered = fast.length > 0 ? fast : data.voices.filter((v) => v.quality === "medium");
    const onlySlow = fast.length === 0 && offered.length > 0;
    const usable = data.voices.filter((v) => offered.includes(v) || v.installed || inUseKeys.has(v.key));
    const hidden = data.voices.length - usable.length;
    const rows = usable.map((v) => renderRow(v));

    // "Suggest a similar voice": for a voice already chosen in another language, the closest pitch in this one.
    const suggestions = h("div", {});
    const suggestButtons = data.in_use.filter((u) => u.language !== data.language).map((u) => {
      const btn = button(t("voices.suggest", { name: voiceName(u.key), language: langName(data.language) }), { variant: "secondary" });
      btn.addEventListener("click", async () => {
        btn.disabled = true;
        clear(suggestions);
        suggestions.append(note(t("voices.suggesting", { n: MAX_SUGGESTION_DEMOS })));
        try {
          const res = await api<Similarity>("GET", `/api/voices/${encodeURIComponent(u.key)}/similar?lang=${encodeURIComponent(data.language)}`);
          clear(suggestions);
          suggestions.append(
            res.matches.length > 0
              ? list(...res.matches.map((m) => renderRow(m.voice, t("voices.pitchDiff", { hz: Math.round(m.diff_hz) }))))
              : note(t("voices.suggestNone")),
            note(t("voices.suggestNote")),
            ...(res.capped ? [note(t("voices.suggestCapped", { n: res.analysed }))] : []),
            ...(res.slower > 0 ? [note(t("voices.suggestSlower", { n: res.slower }))] : []));
        } catch (e) {
          clear(suggestions);
          toast(errorMessage(e), true);
        }
        btn.disabled = false;
      });
      return btn;
    });
    const summary = data.in_use.map((u) => `${langName(u.language)}: ${voiceName(u.key)}`).join(" · ");
    clear(body);
    body.append(field(t("voices.language"), langSelect), ...(summary ? [note(t("voices.inUseSummary", { list: summary }))] : []),
      ...(data.piper_installed ? [] : [note(t("voices.piperMissing"))]), ...(onlySlow ? [note(t("voices.onlySlow"))] : []), progress,
      rows.length > 0 ? list(...rows) : emptyState(hidden > 0 ? t("voices.noneFast", { n: hidden }) : t("voices.none")),
      ...(suggestButtons.length > 0 ? [actions(...suggestButtons), suggestions] : []));
    if (busy) {
      const tick = async (): Promise<void> => {
        if (!body.isConnected) return;
        const j = await api<VoiceJob>("GET", "/api/voices/status").catch(() => null);
        if (!j) return;
        showProgress(j);
        if (j.running) { window.setTimeout(() => void tick(), 1000); return; }
        if (j.error) toast(t("voices.failed", { error: j.error }), true); else toast(t("voices.ready"));
        await load(data.language);
      };
      window.setTimeout(() => void tick(), 1000);
    }
  };
  void load(initialLanguage);
  return card(t("voices.title"), note(t("voices.help")), body);
}


export async function render(root: HTMLElement): Promise<void> {
  const [state, locales] = await Promise.all([api<State>("GET", "/api/assistant"), loadLocales()]);
  clear(root);
  const refresh = () => render(root);
  const s = state.settings;
  const ready = s.tts_engine === "none" ? false : s.tts_engine === "cloud" ? state.tts_key_configured : s.tts_model_path !== "";
  root.append(card(t("voice.title"),
    h("p", {}, ready ? badge(t("voice.ready")) : badge(s.tts_engine === "none" ? t("voice.off") : t("voice.pick"), true)),
    note(t("voice.help"))));

  root.append(voicesCard((s.language && s.language !== AUTO ? s.language : getLocale()).split(/[-_]/)[0]));

  const ttsLangs: [string, string][] = [["", t("assistant.ttsLanguageFollow")], ...locales.map((l): [string, string] => [l.code, l.name])];
  if (s.tts_language && !ttsLangs.some(([v]) => v === s.tts_language)) ttsLangs.push([s.tts_language, s.tts_language]);
  const f = {
    tts: select(t("assistant.ttsEngine"), [["piper", t("assistant.ttsPiper")], ["cloud", t("assistant.ttsCloud")], ["none", t("assistant.ttsNone")]], s.tts_engine),
    ttsUrl: h("input", { type: "text", value: s.tts_base_url, ariaLabel: t("assistant.ttsUrl") }),
    ttsModel: h("input", { type: "text", value: s.tts_model, ariaLabel: t("assistant.ttsModel") }),
    ttsVoice: h("input", { type: "text", value: s.tts_voice, ariaLabel: t("assistant.ttsVoice") }),
    voice: h("input", { type: "text", value: s.tts_model_path, placeholder: "/var/lib/milo/voices/es_ES-davefx-medium.onnx", ariaLabel: t("assistant.piperFile") }),
    ttsLanguage: select(t("assistant.ttsLanguage"), ttsLangs, s.tts_language ?? ""),
    ttsSpeed: h("input", { type: "number", value: String(s.tts_speed ?? 1), min: "0.5", max: "2", step: "0.1", ariaLabel: t("assistant.ttsSpeed") }),
    ttsVolume: h("input", { type: "number", value: String(s.tts_volume ?? 100), min: "0", max: "100", ariaLabel: t("assistant.ttsVolume") }),
  };
  const ttsKey = h("input", { type: "password", autocomplete: "off", placeholder: state.tts_key_configured ? t("assistant.keySaved") : t("assistant.ttsKeyHint"), ariaLabel: t("assistant.ttsKey") });
  // Only the fields of the chosen engine are shown: the rest would just be noise.
  const ttsPiper = h("div", {}, fields(field(t("assistant.piperFile"), f.voice)), note(t("assistant.piperPickHint")));
  const ttsCloud = h("div", {}, fields(field(t("assistant.ttsUrl"), f.ttsUrl), field(t("assistant.ttsModel"), modelField("tts", f.ttsModel)), field(t("assistant.ttsVoice"), f.ttsVoice), field(t("assistant.ttsKeyLabel"), ttsKey)), note(t("assistant.modelsHelp")));
  const ttsTuning = fields(field(t("assistant.ttsLanguage"), f.ttsLanguage), field(t("assistant.ttsSpeed"), f.ttsSpeed), field(t("assistant.ttsVolume"), f.ttsVolume));
  const sync = () => showOnly({ piper: [ttsPiper, ttsTuning], cloud: [ttsCloud, ttsTuning], none: [] }, f.tts.value);
  f.tts.addEventListener("change", sync);
  sync();
  root.append(formCard(async (ev) => {
    ev.preventDefault();
    await guard(async () => {
      await saveSettings(s, {
        tts_engine: f.tts.value, tts_model_path: f.voice.value.trim(), tts_base_url: f.ttsUrl.value.trim(), tts_model: f.ttsModel.value.trim(), tts_voice: f.ttsVoice.value.trim(),
        tts_language: f.ttsLanguage.value, tts_speed: Number(f.ttsSpeed.value), tts_volume: Number(f.ttsVolume.value),
      });
      if (ttsKey.value.trim()) await api("PUT", "/api/assistant/keys", { llm_api_key: "", stt_api_key: "", tts_api_key: ttsKey.value.trim() });
      await refresh();
    }, "common.saved");
  },
    h("h2", {}, t("assistant.voice")),
    fields(field(t("assistant.ttsEngineLabel"), f.tts)),
    ttsPiper, ttsCloud, ttsTuning,
    actions(submit(t("assistant.save")))));
}
