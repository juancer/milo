import { button } from "../../components/ui";
import { t } from "../../i18n";
import { api } from "../api";
import { h } from "../dom";
import { guard, toast } from "../ui";

/** What the Assistant, AI connection and Voice sheets share: the settings object and a few widgets. */
export interface Settings {
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
export interface State {
  settings: Settings;
  status: { enabled: boolean; running: boolean; last_error?: string };
  llm_key_configured: boolean;
  stt_key_configured: boolean;
  tts_key_configured: boolean;
}

/** Saves some settings: the server takes the whole object, so the rest is sent back as it was. */
export const saveSettings = (current: Settings, changes: Partial<Settings>): Promise<unknown> =>
  api("PUT", "/api/assistant", { ...current, ...changes });

/** A text field for a model name with a button that asks the provider which models it has. */
export function modelField(kind: "llm" | "stt" | "tts", input: HTMLInputElement): HTMLElement {
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
export function showOnly(blocks: Record<string, HTMLElement[]>, current: string): void {
  for (const [engine, els] of Object.entries(blocks)) for (const el of els) el.style.display = engine === current ? "" : "none";
}

