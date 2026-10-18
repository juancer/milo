import { actions, badge, button, card, checkbox, emptyState, field, fields, formCard, list, note, row, select, submit, toggle } from "../../components/ui";
import { getLocale, t, tOr } from "../../i18n";
import { api } from "../api";
import { clear, errorMessage, h, when } from "../dom";
import { AUTO, loadLocales } from "../locales";
import { confirmDialog, guard, toast } from "../ui";

interface Settings {
  child_name: string;
  age: number;
  personality: string;
  language: string; // a locale code, or "auto" to answer in the language spoken
  max_response_words: number;
  extra_instructions: string;
  retention_days: number;
  input_moderation: boolean;
  output_moderation: boolean;
  alert_parents: boolean;
  follow_up: boolean;
  max_turns_per_hour: number;
  llm_base_url: string;
  llm_model: string;
  stt_engine: string;
  stt_base_url: string;
  stt_model: string;
  stt_model_path: string;
  tts_engine: string;
  tts_model_path: string;
  tts_base_url: string;
  tts_model: string;
  tts_voice: string;
  tts_language?: string; // "" = same as the assistant language
  tts_speed?: number; // 1 = normal
  tts_volume?: number; // 0-100
}
interface State {
  settings: Settings;
  status: { enabled: boolean; running: boolean; last_error?: string };
  llm_key_configured: boolean;
  stt_key_configured: boolean;
  tts_key_configured: boolean;
}
interface Rule {
  id: number;
  pattern: string;
  applies_to: string;
}
interface Conversation {
  id: number;
  started_at: string;
  source: string;
  lines: { role: string; content: string; tool?: string; at: string }[];
}

interface VoiceRow { key: string; name: string; quality: string; language: string; country: string; speakers: number; size_bytes: number; installed: boolean; fast: boolean }
interface VoiceJob { key: string; running: boolean; stage: string; done: number; total: number; error?: string }
interface VoiceList { language: string; languages: { code: string; name: string }[]; voices: VoiceRow[]; current: string; in_use: { language: string; key: string }[]; piper_installed: boolean; job: VoiceJob }
interface Similarity { source_f0: number; matches: { voice: VoiceRow; f0: number; diff_hz: number }[]; considered: number; analysed: number; capped: boolean; slower: number }

/** "davefx" from "es_ES-davefx-medium". */
const voiceName = (key: string): string => key.split("-").slice(1, -1).join("-") || key;
/** The most demos one suggestion downloads; the server enforces the same cap. */
const MAX_SUGGESTION_DEMOS = 12;

const mb = (bytes: number): string => (bytes / 1048576).toFixed(bytes < 10485760 ? 1 : 0);

/** A text field for a model name with a button that asks the provider which models it has. */
function modelField(kind: "llm" | "stt" | "tts", input: HTMLInputElement): HTMLElement {
  const options = h("datalist", { id: `models-${kind}` });
  input.setAttribute("list", options.id);
  input.autocomplete = "off";
  const load = button(t("assistant.listModels"), {
    variant: "secondary",
    onclick: () => guard(async () => {
      const ids = await api<string[]>("GET", `/api/assistant/models?kind=${kind}`);
      options.replaceChildren(...ids.map((id) => h("option", { value: id })));
      toast(t("assistant.modelsLoaded", { n: ids.length }));
    }),
  });
  return h("div", { class: "inline" }, input, options, load);
}

/** Shows the fields of the chosen engine and hides the ones that do not apply to it. */
function showOnly(blocks: Record<string, HTMLElement[]>, current: string): void {
  for (const [engine, els] of Object.entries(blocks)) for (const el of els) el.style.display = engine === current ? "" : "none";
}

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
    // Only voices that speak fast enough on a Raspberry Pi 3 are offered, plus any already installed or in use.
    const usable = data.voices.filter((v) => v.fast || v.installed || inUseKeys.has(v.key));
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
      ...(data.piper_installed ? [] : [note(t("voices.piperMissing"))]), progress,
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
  const [state, rules, convs, locales] = await Promise.all([
    api<State>("GET", "/api/assistant"),
    api<Rule[]>("GET", "/api/moderation-rules"),
    api<Conversation[]>("GET", "/api/conversations"),
    loadLocales(),
  ]);
  clear(root);
  const refresh = () => render(root);
  const s = state.settings;
  const saved = t("assistant.keySaved");

  // ---- status and keys
  const llmKey = h("input", { type: "password", autocomplete: "off", placeholder: state.llm_key_configured ? saved : "sk-or-…", ariaLabel: t("assistant.llmKey") });
  const sttKey = h("input", { type: "password", autocomplete: "off", placeholder: state.stt_key_configured ? saved : "gsk_…", ariaLabel: t("assistant.sttKey") });
  const ttsKey = h("input", { type: "password", autocomplete: "off", placeholder: state.tts_key_configured ? saved : t("assistant.ttsKeyHint"), ariaLabel: t("assistant.ttsKey") });
  const st = state.status;

  // ---- quick start: one free Groq key does speech recognition and the AI model
  const groqKey = h("input", { type: "password", autocomplete: "off", placeholder: "gsk_…", ariaLabel: t("assistant.groqKey") });
  const groqReady = state.llm_key_configured && state.stt_key_configured;
  root.append(h("details", { open: !groqReady },
    h("summary", {}, t("assistant.groqTitle")),
    card(t("assistant.groqTitle"),
      note(t("assistant.groqHelp")),
      h("form", {
        onsubmit: async (ev: Event) => {
          ev.preventDefault();
          const key = groqKey.value.trim();
          if (!key) return;
          await guard(async () => {
            // Providers first: changing a provider's address drops its stored key, so the key goes in after.
            await api("PUT", "/api/assistant", {
              ...s,
              llm_base_url: "https://api.groq.com/openai/v1", llm_model: "openai/gpt-oss-20b",
              stt_engine: "cloud", stt_base_url: "https://api.groq.com/openai/v1", stt_model: "whisper-large-v3-turbo",
            });
            await api("PUT", "/api/assistant/keys", { llm_api_key: key, stt_api_key: key, tts_api_key: "" });
            await api("PUT", "/api/features/assistant", { enabled: true });
            await refresh();
          }, "assistant.groqDone");
        },
      }, fields(field(t("assistant.groqKeyLabel"), groqKey), submit(t("assistant.groqUse")))))));

  root.append(card(t("assistant.title"),
    h("p", {}, !st.enabled ? badge(t("common.featureOff"), true) : st.running ? badge(t("common.running")) : badge(t("common.notRunning"), true), " ",
      st.enabled ? null : h("span", { class: "muted" }, t("assistant.turnOn")),
      st.last_error ? h("span", { class: "error" }, ` ${st.last_error}`) : null),
    note(t("assistant.keysHelp")),
    h("form", {
      onsubmit: async (ev: Event) => {
        ev.preventDefault();
        if (await guard(() => api("PUT", "/api/assistant/keys", { llm_api_key: llmKey.value.trim(), stt_api_key: sttKey.value.trim(), tts_api_key: ttsKey.value.trim() }), "assistant.keysSaved")) await refresh();
      },
    }, fields(field(t("assistant.llmKeyLabel"), llmKey), field(t("assistant.sttKeyLabel"), sttKey), field(t("assistant.ttsKeyLabel"), ttsKey), submit(t("assistant.saveKeys"))))));

  root.append(voicesCard((s.language && s.language !== AUTO ? s.language : getLocale()).split(/[-_]/)[0]));

  // ---- language options: locales, "auto" (assistant only) and any legacy free-text value already saved
  const localeOptions = locales.map((l): [string, string] => [l.code, l.name]);
  const assistantLangs: [string, string][] = [[AUTO, t("lang.auto")], ...localeOptions];
  if (s.language && !assistantLangs.some(([v]) => v === s.language)) assistantLangs.push([s.language, s.language]);
  const ttsLangs: [string, string][] = [["", t("assistant.ttsLanguageFollow")], ...localeOptions];
  if (s.tts_language && !ttsLangs.some(([v]) => v === s.tts_language)) ttsLangs.push([s.tts_language, s.tts_language]);

  // ---- personality and safety
  const f = {
    child: h("input", { type: "text", value: s.child_name, maxLength: 40, ariaLabel: t("assistant.childName") }),
    age: h("input", { type: "number", value: String(s.age), min: "3", max: "17", ariaLabel: t("assistant.age") }),
    personality: h("input", { type: "text", value: s.personality, maxLength: 200, ariaLabel: t("assistant.personality") }),
    language: select(t("assistant.language"), assistantLangs, s.language || AUTO),
    words: h("input", { type: "number", value: String(s.max_response_words), min: "10", max: "300", ariaLabel: t("assistant.maxWords") }),
    extra: h("textarea", { rows: 3, maxLength: 600, ariaLabel: t("assistant.extra"), value: s.extra_instructions }),
    retention: select(t("assistant.history"), [["0", t("assistant.keepNone")], ["7", t("assistant.days", { n: 7 })], ["30", t("assistant.days", { n: 30 })], ["90", t("assistant.days", { n: 90 })]], String(s.retention_days)),
    input: checkbox(s.input_moderation),
    output: checkbox(s.output_moderation),
    alert: checkbox(s.alert_parents),
    follow: checkbox(s.follow_up),
    turns: h("input", { type: "number", value: String(s.max_turns_per_hour), min: "5", max: "600", ariaLabel: t("assistant.turnsPerHour") }),
    llmUrl: h("input", { type: "text", value: s.llm_base_url, ariaLabel: t("assistant.llmUrl") }),
    llmModel: h("input", { type: "text", value: s.llm_model, ariaLabel: t("assistant.llmModel") }),
    sttUrl: h("input", { type: "text", value: s.stt_base_url, ariaLabel: t("assistant.sttUrl") }),
    sttModel: h("input", { type: "text", value: s.stt_model, ariaLabel: t("assistant.sttModel") }),
    sttEngine: select(t("assistant.sttEngine"), [["cloud", t("assistant.sttCloud")], ["whispercpp", t("assistant.sttLocal")]], s.stt_engine),
    sttPath: h("input", { type: "text", value: s.stt_model_path, placeholder: "/opt/whisper/ggml-tiny.bin", ariaLabel: t("assistant.sttPath") }),
    tts: select(t("assistant.ttsEngine"), [["piper", t("assistant.ttsPiper")], ["cloud", t("assistant.ttsCloud")], ["none", t("assistant.ttsNone")]], s.tts_engine),
    ttsUrl: h("input", { type: "text", value: s.tts_base_url, ariaLabel: t("assistant.ttsUrl") }),
    ttsModel: h("input", { type: "text", value: s.tts_model, ariaLabel: t("assistant.ttsModel") }),
    ttsVoice: h("input", { type: "text", value: s.tts_voice, ariaLabel: t("assistant.ttsVoice") }),
    voice: h("input", { type: "text", value: s.tts_model_path, placeholder: "/opt/piper/en_US-lessac-medium.onnx", ariaLabel: t("assistant.piperFile") }),
    ttsLanguage: select(t("assistant.ttsLanguage"), ttsLangs, s.tts_language ?? ""),
    ttsSpeed: h("input", { type: "number", value: String(s.tts_speed ?? 1), min: "0.5", max: "2", step: "0.1", ariaLabel: t("assistant.ttsSpeed") }),
    ttsVolume: h("input", { type: "number", value: String(s.tts_volume ?? 100), min: "0", max: "100", ariaLabel: t("assistant.ttsVolume") }),
  };
  // Only the fields of the chosen engine are shown: the rest would just be noise.
  const sttCloud = fields(field(t("assistant.sttUrl"), f.sttUrl), field(t("assistant.sttModel"), modelField("stt", f.sttModel)));
  const sttLocal = fields(field(t("assistant.sttPath"), f.sttPath));
  const ttsPiper = h("div", {}, fields(field(t("assistant.piperFile"), f.voice)), note(t("assistant.piperPickHint")));
  const ttsCloud = fields(field(t("assistant.ttsUrl"), f.ttsUrl), field(t("assistant.ttsModel"), modelField("tts", f.ttsModel)), field(t("assistant.ttsVoice"), f.ttsVoice));
  const ttsTuning = fields(field(t("assistant.ttsLanguage"), f.ttsLanguage), field(t("assistant.ttsSpeed"), f.ttsSpeed), field(t("assistant.ttsVolume"), f.ttsVolume));
  const syncEngines = () => {
    showOnly({ cloud: [sttCloud], whispercpp: [sttLocal] }, f.sttEngine.value);
    showOnly({ piper: [ttsPiper, ttsTuning], cloud: [ttsCloud, ttsTuning], none: [] }, f.tts.value);
  };
  f.sttEngine.addEventListener("change", syncEngines);
  f.tts.addEventListener("change", syncEngines);
  syncEngines();
  root.append(formCard(async (ev) => {
    ev.preventDefault();
    await guard(async () => {
      await api("PUT", "/api/assistant", {
        child_name: f.child.value.trim(), age: Number(f.age.value), personality: f.personality.value.trim(), language: f.language.value,
        max_response_words: Number(f.words.value), extra_instructions: f.extra.value.trim(), retention_days: Number(f.retention.value),
        input_moderation: f.input.checked, output_moderation: f.output.checked, alert_parents: f.alert.checked, follow_up: f.follow.checked,
        max_turns_per_hour: Number(f.turns.value), llm_base_url: f.llmUrl.value.trim(), llm_model: f.llmModel.value.trim(),
        stt_engine: f.sttEngine.value, stt_base_url: f.sttUrl.value.trim(), stt_model: f.sttModel.value.trim(), stt_model_path: f.sttPath.value.trim(),
        tts_engine: f.tts.value, tts_model_path: f.voice.value.trim(), tts_base_url: f.ttsUrl.value.trim(), tts_model: f.ttsModel.value.trim(), tts_voice: f.ttsVoice.value.trim(),
        tts_language: f.ttsLanguage.value, tts_speed: Number(f.ttsSpeed.value), tts_volume: Number(f.ttsVolume.value),
      });
      await refresh();
    }, "common.saved");
  },
    h("h2", {}, t("assistant.personality")),
    fields(field(t("assistant.childNameLabel"), f.child), field(t("assistant.age"), f.age), field(t("assistant.personality"), f.personality), field(t("assistant.language"), f.language), field(t("assistant.maxWordsLabel"), f.words)),
    note(t("assistant.languageHelp")),
    field(t("assistant.extraLabel"), f.extra),
    h("h2", {}, t("assistant.safety")),
    note(t("assistant.safetyHelp")),
    toggle(t("assistant.checkInput"), f.input),
    toggle(t("assistant.checkOutput"), f.output),
    toggle(t("assistant.alertParents"), f.alert),
    toggle(t("assistant.followUp"), f.follow),
    fields(field(t("assistant.historyLabel"), f.retention), field(t("assistant.turnsLabel"), f.turns)),
    h("h2", {}, t("assistant.providers")),
    note(t("assistant.providersHelp")),
    fields(field(t("assistant.llmUrl"), f.llmUrl), field(t("assistant.llmModel"), modelField("llm", f.llmModel))),
    note(t("assistant.modelsHelp")),
    fields(field(t("assistant.sttEngine"), f.sttEngine)),
    sttCloud, sttLocal,
    h("h2", {}, t("assistant.voice")),
    fields(field(t("assistant.ttsEngineLabel"), f.tts)),
    ttsPiper, ttsCloud, ttsTuning,
    actions(submit(t("assistant.save")))));

  // ---- blocked phrases
  const pattern = h("input", { type: "text", maxLength: 80, placeholder: t("assistant.phraseHint"), ariaLabel: t("assistant.blockedPhrase") });
  const where = select(t("assistant.appliesTo"), [["both", t("assistant.appliesBoth")], ["input", t("assistant.appliesInput")], ["output", t("assistant.appliesOutput")]]);
  root.append(card(t("assistant.blocked"),
    note(t("assistant.blockedHelp")),
    h("form", {
      class: "inline",
      onsubmit: async (ev: Event) => {
        ev.preventDefault();
        if (await guard(() => api("POST", "/api/moderation-rules", { pattern: pattern.value.trim(), applies_to: where.value }))) await refresh();
      },
    }, pattern, where, h("button", { type: "submit" }, t("assistant.block"))),
    list(...rules.map((r) => row(
      h("span", {}, `“${r.pattern}” `, badge(tOr(`assistant.applies.${r.applies_to}`, r.applies_to))),
      button(t("common.remove"), { variant: "secondary", onclick: async () => { if (await guard(() => api("DELETE", `/api/moderation-rules/${r.id}`))) await refresh(); } }))))));

  // ---- conversations
  root.append(card(t("assistant.conversations"),
    note(s.retention_days ? t("assistant.keptFor", { days: s.retention_days }) : t("assistant.notKept")),
    convs.length === 0 ? emptyState(t("assistant.noConversations")) : null,
    ...convs.map((c) => h("details", {}, h("summary", {}, t("assistant.conversationSummary", { when: when(c.started_at), source: c.source, count: c.lines.length })),
      list(...c.lines.map((l) => row(
        h("span", {}, h("strong", {}, l.role === "user" ? t("assistant.roleUser") : l.role === "assistant" ? t("assistant.roleAssistant") : t("assistant.roleTool", { tool: l.tool ?? "" })), " ", l.content)))))),
    convs.length ? actions(button(t("assistant.eraseAll"), {
      variant: "danger",
      onclick: async () => {
        if (!(await confirmDialog(t("assistant.confirmErase"), t("assistant.eraseAll")))) return;
        if (await guard(() => api("DELETE", "/api/conversations"))) await refresh();
      },
    })) : null));
}
