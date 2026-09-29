import { api } from "../api";
import { badge, clear, confirmTwice, errorMessage, field, h, toast, when } from "../dom";

interface Settings {
  child_name: string;
  age: number;
  personality: string;
  language: string;
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
  stt_base_url: string;
  stt_model: string;
  tts_engine: string;
  tts_model_path: string;
}
interface State {
  settings: Settings;
  status: { enabled: boolean; running: boolean; last_error?: string };
  llm_key_configured: boolean;
  stt_key_configured: boolean;
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
  const [state, rules, convs] = await Promise.all([
    api<State>("GET", "/api/assistant"),
    api<Rule[]>("GET", "/api/moderation-rules"),
    api<Conversation[]>("GET", "/api/conversations"),
  ]);
  clear(root);
  const refresh = () => render(root);
  const s = state.settings;

  // ---- status and keys
  const llmKey = h("input", { type: "password", autocomplete: "off", placeholder: state.llm_key_configured ? "(saved - type to replace)" : "sk-or-…", ariaLabel: "AI API key" });
  const sttKey = h("input", { type: "password", autocomplete: "off", placeholder: state.stt_key_configured ? "(saved - type to replace)" : "gsk_…", ariaLabel: "Speech-to-text API key" });
  const st = state.status;
  root.append(h("section", { class: "card" }, h("h2", {}, "Assistant"),
    h("p", {}, !st.enabled ? badge("feature off", true) : st.running ? badge("running") : badge("not running", true), " ",
      st.enabled ? null : h("span", { class: "muted" }, "Turn the “assistant” feature on in the Dashboard."),
      st.last_error ? h("span", { class: "error" }, ` ${st.last_error}`) : null),
    h("p", { class: "muted" }, "Keys are stored encrypted on the device and are never shown again. The AI service receives only what the child says as text; the speech service receives only the recording."),
    h("form", {
      onsubmit: async (ev: Event) => {
        ev.preventDefault();
        try {
          await api("PUT", "/api/assistant/keys", { llm_api_key: llmKey.value.trim(), stt_api_key: sttKey.value.trim() });
          toast("Keys saved");
          await refresh();
        } catch (e) {
          toast(errorMessage(e), true);
        }
      },
    }, h("div", { class: "fields" }, field("AI key (OpenRouter, Groq…)", llmKey), field("Speech-to-text key (Groq…)", sttKey), h("button", { type: "submit" }, "Save keys")))));

  // ---- personality and safety
  const f = {
    child: h("input", { type: "text", value: s.child_name, maxLength: 40, ariaLabel: "Child's first name" }),
    age: h("input", { type: "number", value: String(s.age), min: "3", max: "17", ariaLabel: "Age" }),
    personality: h("input", { type: "text", value: s.personality, maxLength: 200, ariaLabel: "Personality" }),
    language: h("input", { type: "text", value: s.language, maxLength: 40, placeholder: "same as the child", ariaLabel: "Language" }),
    words: h("input", { type: "number", value: String(s.max_response_words), min: "10", max: "300", ariaLabel: "Maximum words" }),
    extra: h("textarea", { rows: 3, maxLength: 600, ariaLabel: "Extra instructions", value: s.extra_instructions }),
    retention: h("select", { ariaLabel: "History" }, ...[[0, "Do not keep"], [7, "7 days"], [30, "30 days"], [90, "90 days"]].map(([v, l]) => h("option", { value: String(v), selected: v === s.retention_days }, String(l)))),
    input: h("input", { type: "checkbox", checked: s.input_moderation }),
    output: h("input", { type: "checkbox", checked: s.output_moderation }),
    alert: h("input", { type: "checkbox", checked: s.alert_parents }),
    follow: h("input", { type: "checkbox", checked: s.follow_up }),
    turns: h("input", { type: "number", value: String(s.max_turns_per_hour), min: "5", max: "600", ariaLabel: "Questions per hour" }),
    llmUrl: h("input", { type: "text", value: s.llm_base_url, ariaLabel: "AI address" }),
    llmModel: h("input", { type: "text", value: s.llm_model, ariaLabel: "AI model" }),
    sttUrl: h("input", { type: "text", value: s.stt_base_url, ariaLabel: "Speech-to-text address" }),
    sttModel: h("input", { type: "text", value: s.stt_model, ariaLabel: "Speech-to-text model" }),
    tts: h("select", { ariaLabel: "Text to speech" },
      h("option", { value: "piper", selected: s.tts_engine === "piper" }, "Piper (on this device)"),
      h("option", { value: "none", selected: s.tts_engine === "none" }, "Off (tones only)")),
    voice: h("input", { type: "text", value: s.tts_model_path, placeholder: "/opt/piper/en_US-lessac-medium.onnx", ariaLabel: "Piper voice file" }),
  };
  root.append(h("form", {
    class: "card",
    onsubmit: async (ev: Event) => {
      ev.preventDefault();
      try {
        await api("PUT", "/api/assistant", {
          child_name: f.child.value.trim(), age: Number(f.age.value), personality: f.personality.value.trim(), language: f.language.value.trim(),
          max_response_words: Number(f.words.value), extra_instructions: f.extra.value.trim(), retention_days: Number(f.retention.value),
          input_moderation: f.input.checked, output_moderation: f.output.checked, alert_parents: f.alert.checked, follow_up: f.follow.checked,
          max_turns_per_hour: Number(f.turns.value), llm_base_url: f.llmUrl.value.trim(), llm_model: f.llmModel.value.trim(),
          stt_base_url: f.sttUrl.value.trim(), stt_model: f.sttModel.value.trim(), tts_engine: f.tts.value, tts_model_path: f.voice.value.trim(),
        });
        toast("Saved");
        await refresh();
      } catch (e) {
        toast(errorMessage(e), true);
      }
    },
  },
    h("h2", {}, "Personality"),
    h("div", { class: "fields" }, field("Child's first name (optional)", f.child), field("Age", f.age), field("Personality", f.personality), field("Language", f.language), field("Max words per answer", f.words)),
    field("Extra instructions for the assistant", f.extra),
    h("h2", {}, "Safety"),
    h("p", { class: "muted" }, "Nexo checks what the child says and what the AI answers with its own rules, on top of the AI's own safeguards. This is a safety net, not a guarantee: keep an eye on the conversations below."),
    h("label", { class: "check" }, f.input, " Check what the child says before the AI sees it"),
    h("label", { class: "check" }, f.output, " Check the AI's answers before the child hears them"),
    h("label", { class: "check" }, f.alert, " Message the parents on Telegram if the child says something worrying"),
    h("label", { class: "check" }, f.follow, " Keep listening for a follow-up after each answer"),
    h("div", { class: "fields" }, field("Keep conversation history", f.retention), field("Questions per hour (limits cost and free-tier use)", f.turns)),
    h("h2", {}, "Providers"),
    h("p", { class: "muted" }, "Any OpenAI-compatible service works. Defaults: OpenRouter for the AI and Groq for speech recognition. Text-to-speech runs locally with Piper."),
    h("div", { class: "fields" }, field("AI address", f.llmUrl), field("AI model", f.llmModel), field("Speech-to-text address", f.sttUrl), field("Speech-to-text model", f.sttModel)),
    h("div", { class: "fields" }, field("Voice (text-to-speech)", f.tts), field("Piper voice file (.onnx)", f.voice)),
    h("div", { class: "actions" }, h("button", { type: "submit" }, "Save settings"))));

  // ---- blocked phrases
  const pattern = h("input", { type: "text", maxLength: 80, placeholder: "a word or phrase", ariaLabel: "Blocked phrase" });
  const where = h("select", { ariaLabel: "Applies to" }, ...[["both", "What the child says and what the AI says"], ["input", "Only what the child says"], ["output", "Only what the AI says"]].map(([v, l]) => h("option", { value: v }, l)));
  root.append(h("section", { class: "card" }, h("h2", {}, "Blocked words and phrases"),
    h("p", { class: "muted" }, "Anything containing these is never sent to the AI or never spoken. The child hears a friendly “let's talk about something else”."),
    h("form", {
      class: "inline",
      onsubmit: async (ev: Event) => {
        ev.preventDefault();
        try {
          await api("POST", "/api/moderation-rules", { pattern: pattern.value.trim(), applies_to: where.value });
          await refresh();
        } catch (e) {
          toast(errorMessage(e), true);
        }
      },
    }, pattern, where, h("button", { type: "submit" }, "Block")),
    h("ul", { class: "list" }, ...rules.map((r) => h("li", { class: "row" },
      h("span", {}, `“${r.pattern}” `, badge(r.applies_to)),
      h("button", { type: "button", class: "secondary", onclick: async () => { await api("DELETE", `/api/moderation-rules/${r.id}`); await refresh(); } }, "Remove"))))));

  // ---- conversations
  root.append(h("section", { class: "card" }, h("h2", {}, "Conversations"),
    h("p", { class: "muted" }, s.retention_days ? `Kept for ${s.retention_days} days, then deleted automatically.` : "Not kept: history is switched off."),
    convs.length === 0 ? h("p", { class: "muted" }, "No conversations stored.") : null,
    ...convs.map((c) => h("details", {}, h("summary", {}, `${when(c.started_at)} · ${c.source} · ${c.lines.length} lines`),
      h("ul", { class: "list" }, ...c.lines.map((l) => h("li", { class: "row" },
        h("span", {}, h("strong", {}, l.role === "user" ? "Child: " : l.role === "assistant" ? "Nexo: " : `tool ${l.tool ?? ""}: `), l.content)))))),
    convs.length ? h("div", { class: "actions" }, h("button", {
      type: "button", class: "danger",
      onclick: async (ev: Event) => {
        if (!confirmTwice(ev.currentTarget as HTMLButtonElement, "Erase all")) return;
        await api("DELETE", "/api/conversations");
        await refresh();
      },
    }, "Erase all conversations")) : null));
}
