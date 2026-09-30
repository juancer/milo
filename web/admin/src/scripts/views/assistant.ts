import { actions, badge, button, card, checkbox, emptyState, field, fields, formCard, list, note, row, select, submit, toggle } from "../../components/ui";
import { t, tOr } from "../../i18n";
import { api } from "../api";
import { clear, h, when } from "../dom";
import { AUTO, loadLocales } from "../locales";
import { confirmDialog, guard } from "../ui";

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
    fields(field(t("assistant.llmUrl"), f.llmUrl), field(t("assistant.llmModel"), f.llmModel), field(t("assistant.sttUrl"), f.sttUrl), field(t("assistant.sttModel"), f.sttModel)),
    fields(field(t("assistant.sttEngine"), f.sttEngine), field(t("assistant.sttPath"), f.sttPath)),
    h("h2", {}, t("assistant.voice")),
    fields(field(t("assistant.ttsEngineLabel"), f.tts), field(t("assistant.piperFile"), f.voice)),
    fields(field(t("assistant.ttsUrl"), f.ttsUrl), field(t("assistant.ttsModel"), f.ttsModel), field(t("assistant.ttsVoice"), f.ttsVoice)),
    fields(field(t("assistant.ttsLanguage"), f.ttsLanguage), field(t("assistant.ttsSpeed"), f.ttsSpeed), field(t("assistant.ttsVolume"), f.ttsVolume)),
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
