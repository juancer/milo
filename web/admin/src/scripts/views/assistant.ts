import { actions, badge, button, card, checkbox, emptyState, field, fields, formCard, list, note, row, select, submit, toggle } from "../../components/ui";
import { t, tOr } from "../../i18n";
import { api } from "../api";
import { clear, h, when } from "../dom";
import { AUTO, loadLocales } from "../locales";
import { confirmDialog, guard } from "../ui";
import { saveSettings, type State } from "./assistantShared";

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
  const st = state.status;

  root.append(card(t("assistant.title"),
    h("p", {}, !st.enabled ? badge(t("common.featureOff"), true) : st.running ? badge(t("common.running")) : badge(t("common.notRunning"), true), " ",
      st.enabled ? null : h("span", { class: "muted" }, t("assistant.turnOn")),
      st.last_error ? h("span", { class: "error" }, ` ${st.last_error}`) : null),
    note(t("assistant.behaviourHelp")),
    ...(state.llm_key_configured ? [] : [h("p", { class: "error" }, t("assistant.needAiFirst"))])));

  // ---- language options: locales, "auto" and any legacy free-text value already saved
  const assistantLangs: [string, string][] = [[AUTO, t("lang.auto")], ...locales.map((l): [string, string] => [l.code, l.name])];
  if (s.language && !assistantLangs.some(([v]) => v === s.language)) assistantLangs.push([s.language, s.language]);

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
  };
  root.append(formCard(async (ev) => {
    ev.preventDefault();
    await guard(async () => {
      await saveSettings(s, {
        child_name: f.child.value.trim(), age: Number(f.age.value), personality: f.personality.value.trim(), language: f.language.value,
        max_response_words: Number(f.words.value), extra_instructions: f.extra.value.trim(), retention_days: Number(f.retention.value),
        input_moderation: f.input.checked, output_moderation: f.output.checked, alert_parents: f.alert.checked, follow_up: f.follow.checked,
        max_turns_per_hour: Number(f.turns.value),
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
