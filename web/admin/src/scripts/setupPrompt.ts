/**
 * Turning a feature on when it still needs configuration: instead of an error, ask for exactly what
 * is missing, save it, then enable the feature. Cancelling leaves everything as it was.
 */
import { t } from "../i18n";
import { api } from "./api";
import { promptDialog, type PromptConfig } from "./ui";

/** What the person must still provide before `feature` can run, or null if nothing (or unknown). */
async function missing(feature: string): Promise<Omit<PromptConfig, "submitLabel" | "run"> & { save: (v: string[]) => Promise<void> } | null> {
  if (feature === "telegram") {
    const st = await api<{ configured: boolean }>("GET", "/api/telegram");
    if (st.configured) return null;
    return {
      title: t("prompt.telegram.title"),
      intro: t("prompt.telegram.intro"),
      fields: [{ label: t("telegram.botToken"), type: "password", placeholder: "123456:ABC-DEF…" }],
      save: async ([token]) => void (await api("PUT", "/api/telegram/token", { token })),
    };
  }
  if (feature === "assistant") {
    const st = await api<{ llm_key_configured: boolean; stt_key_configured: boolean; settings: { stt_engine: string } }>("GET", "/api/assistant");
    const needLLM = !st.llm_key_configured;
    const needSTT = st.settings.stt_engine === "cloud" && !st.stt_key_configured;
    if (!needLLM && !needSTT) return null;
    return {
      title: t("prompt.assistant.title"),
      intro: t("prompt.assistant.intro"),
      fields: [
        ...(needLLM ? [{ label: t("assistant.llmKeyLabel"), type: "password" as const, placeholder: "sk-or-…" }] : []),
        ...(needSTT ? [{ label: t("assistant.sttKeyLabel"), type: "password" as const, placeholder: "gsk_…" }] : []),
      ],
      save: async (values) => {
        const [first, second] = values;
        await api("PUT", "/api/assistant/keys", { llm_api_key: needLLM ? first : "", stt_api_key: needSTT ? (needLLM ? second : first) : "", tts_api_key: "" });
      },
    };
  }
  if (feature === "weather") {
    const st = await api<{ weather: { configured: boolean } }>("GET", "/api/skills");
    if (st.weather.configured) return null;
    const saveUnits = (units: string) => api("PUT", "/api/weather", { place: "", units });
    return {
      title: t("prompt.weather.title"),
      intro: t("prompt.weather.intro"),
      fields: [
        { label: t("prompt.weather.town"), placeholder: t("skills.yourTown"), optional: true },
        { label: t("skills.units"), type: "select", options: [["celsius", t("skills.celsius")], ["fahrenheit", t("skills.fahrenheit")]] },
      ],
      footnote: t("skills.detectPrivacy"),
      // Detecting is the main way in; typing a town is the alternative.
      primary: {
        label: t("skills.detect"),
        run: async ([, units]) => {
          await api("POST", "/api/weather/detect");
          await saveUnits(units);
        },
      },
      save: async ([town, units]) => {
        if (!town) throw new Error(t("prompt.weather.needTown"));
        await api("PUT", "/api/weather", { place: town, units });
      },
    };
  }
  return null;
}

/**
 * Enables a feature. Returns whether it ended up enabled; false means the person cancelled the
 * setup prompt (nothing changed, nothing reported).
 */
export async function enableFeature(feature: string): Promise<boolean> {
  const need = await missing(feature);
  if (need) {
    const { save, ...rest } = need;
    const saved = await promptDialog({ ...rest, submitLabel: t("prompt.saveAndEnable"), run: save });
    if (!saved) return false;
  }
  await api("PUT", `/api/features/${encodeURIComponent(feature)}`, { enabled: true });
  return true;
}

/** True if `feature` has a setup prompt (used to decide whether a "Set up" button can do something). */
export async function hasSetupPrompt(feature: string): Promise<boolean> {
  return (await missing(feature).catch(() => null)) !== null;
}

