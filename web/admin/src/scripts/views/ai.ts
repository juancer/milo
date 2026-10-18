import { actions, badge, card, field, fields, formCard, note, select, submit } from "../../components/ui";
import { t } from "../../i18n";
import { api } from "../api";
import { clear, h } from "../dom";
import { guard } from "../ui";
import { modelField, saveSettings, showOnly, type State } from "./assistantShared";

export async function render(root: HTMLElement): Promise<void> {
  const state = await api<State>("GET", "/api/assistant");
  clear(root);
  const refresh = () => render(root);
  const s = state.settings;
  const saved = t("assistant.keySaved");
  const connected = state.llm_key_configured && state.stt_key_configured;

  root.append(card(t("ai.title"),
    h("p", {}, connected ? badge(t("ai.connected")) : badge(t("ai.needsKeys"), true)),
    note(t("ai.help"))));

  // ---- quick start: one free Groq key does speech recognition and the AI model
  const groqKey = h("input", { type: "password", autocomplete: "off", placeholder: "gsk_…", ariaLabel: t("assistant.groqKey") });
  root.append(h("details", { open: !connected },
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
            await saveSettings(s, {
              llm_base_url: "https://api.groq.com/openai/v1", llm_model: "openai/gpt-oss-20b",
              stt_engine: "cloud", stt_base_url: "https://api.groq.com/openai/v1", stt_model: "whisper-large-v3-turbo",
            });
            await api("PUT", "/api/assistant/keys", { llm_api_key: key, stt_api_key: key, tts_api_key: "" });
            await api("PUT", "/api/features/assistant", { enabled: true });
            await refresh();
          }, "assistant.groqDone");
        },
      }, fields(field(t("assistant.groqKeyLabel"), groqKey), submit(t("assistant.groqUse")))))));

  // ---- keys
  const llmKey = h("input", { type: "password", autocomplete: "off", placeholder: state.llm_key_configured ? saved : "sk-or-…", ariaLabel: t("assistant.llmKey") });
  const sttKey = h("input", { type: "password", autocomplete: "off", placeholder: state.stt_key_configured ? saved : "gsk_…", ariaLabel: t("assistant.sttKey") });
  root.append(card(t("ai.keys"),
    note(t("assistant.keysHelp")),
    h("form", {
      onsubmit: async (ev: Event) => {
        ev.preventDefault();
        if (await guard(() => api("PUT", "/api/assistant/keys", { llm_api_key: llmKey.value.trim(), stt_api_key: sttKey.value.trim(), tts_api_key: "" }), "assistant.keysSaved")) await refresh();
      },
    }, fields(field(t("assistant.llmKeyLabel"), llmKey), field(t("assistant.sttKeyLabel"), sttKey), submit(t("assistant.saveKeys"))))));

  // ---- providers
  const f = {
    llmUrl: h("input", { type: "text", value: s.llm_base_url, ariaLabel: t("assistant.llmUrl") }),
    llmModel: h("input", { type: "text", value: s.llm_model, ariaLabel: t("assistant.llmModel") }),
    sttUrl: h("input", { type: "text", value: s.stt_base_url, ariaLabel: t("assistant.sttUrl") }),
    sttModel: h("input", { type: "text", value: s.stt_model, ariaLabel: t("assistant.sttModel") }),
    sttEngine: select(t("assistant.sttEngine"), [["cloud", t("assistant.sttCloud")], ["whispercpp", t("assistant.sttLocal")]], s.stt_engine),
    sttPath: h("input", { type: "text", value: s.stt_model_path, placeholder: "/opt/whisper/ggml-tiny.bin", ariaLabel: t("assistant.sttPath") }),
  };
  // Only the fields of the chosen engine are shown: the rest would just be noise.
  const sttCloud = fields(field(t("assistant.sttUrl"), f.sttUrl), field(t("assistant.sttModel"), modelField("stt", f.sttModel)));
  const sttLocal = fields(field(t("assistant.sttPath"), f.sttPath));
  const sync = () => showOnly({ cloud: [sttCloud], whispercpp: [sttLocal] }, f.sttEngine.value);
  f.sttEngine.addEventListener("change", sync);
  sync();
  root.append(formCard(async (ev) => {
    ev.preventDefault();
    await guard(async () => {
      await saveSettings(s, {
        llm_base_url: f.llmUrl.value.trim(), llm_model: f.llmModel.value.trim(),
        stt_engine: f.sttEngine.value, stt_base_url: f.sttUrl.value.trim(), stt_model: f.sttModel.value.trim(), stt_model_path: f.sttPath.value.trim(),
      });
      await refresh();
    }, "common.saved");
  },
    h("h2", {}, t("assistant.providers")),
    note(t("assistant.providersHelp")),
    fields(field(t("assistant.llmUrl"), f.llmUrl), field(t("assistant.llmModel"), modelField("llm", f.llmModel))),
    note(t("assistant.modelsHelp")),
    fields(field(t("assistant.sttEngine"), f.sttEngine)),
    sttCloud, sttLocal,
    actions(submit(t("assistant.save")))));
}
