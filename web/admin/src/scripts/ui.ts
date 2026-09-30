import { applyI18n, setLocale, t } from "../i18n";
import { errorMessage, h } from "./dom";

/** Shows a short message in the toast area. */
export function toast(message: string, isError = false): void {
  const area = document.getElementById("toasts");
  if (!area) return;
  const el = h("div", { class: isError ? "toast error" : "toast", role: isError ? "alert" : "status" }, message);
  area.appendChild(el);
  setTimeout(() => el.remove(), isError ? 6000 : 3000);
}

/**
 * Runs an action, reports its failure as an error toast and (optionally) its
 * success as a toast. Resolves to whether it succeeded.
 */
export async function guard(fn: () => Promise<unknown>, okKey?: string): Promise<boolean> {
  try {
    await fn();
    if (okKey) toast(t(okKey));
    return true;
  } catch (e) {
    toast(errorMessage(e), true);
    return false;
  }
}

/** Two-step confirmation on the button itself: the first click arms it, the second confirms. */
export function confirmTwice(btn: HTMLButtonElement): boolean {
  if (btn.dataset.armed === "1") return true;
  btn.dataset.armed = "1";
  const original = btn.textContent;
  btn.textContent = t("common.clickAgain");
  setTimeout(() => {
    btn.dataset.armed = "";
    btn.textContent = original;
  }, 4000);
  return false;
}

/**
 * Confirmation in the shell's in-page dialog (not window.confirm: some viewers
 * block native dialogs). Resolves true only if the user confirms.
 */
export function confirmDialog(message: string, confirmLabel: string = t("common.confirm")): Promise<boolean> {
  const dialog = document.getElementById("confirm-dialog");
  const text = document.getElementById("confirm-message");
  const yes = document.getElementById("confirm-yes") as HTMLButtonElement | null;
  const no = document.getElementById("confirm-no") as HTMLButtonElement | null;
  if (!dialog || !text || !yes || !no) return Promise.resolve(false);
  text.textContent = message;
  yes.textContent = confirmLabel;
  dialog.hidden = false;
  yes.focus();
  return new Promise((resolve) => {
    const finish = (answer: boolean) => {
      dialog.hidden = true;
      yes.removeEventListener("click", onYes);
      no.removeEventListener("click", onNo);
      document.removeEventListener("keydown", onKey);
      resolve(answer);
    };
    const onYes = () => finish(true);
    const onNo = () => finish(false);
    const onKey = (ev: KeyboardEvent) => ev.key === "Escape" && finish(false);
    yes.addEventListener("click", onYes);
    no.addEventListener("click", onNo);
    document.addEventListener("keydown", onKey);
  });
}

/** Switches the interface language and asks the shell to repaint everything. */
export function changeLocale(code: string): void {
  setLocale(code);
  applyI18n();
  document.dispatchEvent(new Event("milo:locale"));
}

export interface PromptField {
  label: string;
  type?: "text" | "password" | "select";
  placeholder?: string;
  /** Options of a "select" field, as [value, label]; the first one is chosen by default. */
  options?: [string, string][];
  /** An optional field may be left empty. */
  optional?: boolean;
}
export interface PromptConfig {
  title: string;
  intro: string;
  fields: PromptField[];
  submitLabel: string;
  /** Saves the values. Throwing keeps the dialog open and shows the error inside it. */
  run: (values: string[]) => Promise<void>;
  /** A main action offered above the fields (they become the alternative); it gets the field values too. */
  primary?: { label: string; run: (values: string[]) => Promise<void> };
  /** A small note under the fields. */
  footnote?: string;
}

/**
 * Small in-page form dialog ("what is missing?"). Resolves true once `run` succeeded, false if the
 * person cancelled. Cancelling never reports an error.
 */
export function promptDialog(cfg: PromptConfig): Promise<boolean> {
  const dialog = document.getElementById("prompt-dialog") as HTMLElement;
  const form = document.getElementById("prompt-form") as HTMLFormElement;
  const box = document.getElementById("prompt-fields") as HTMLElement;
  const error = document.getElementById("prompt-error") as HTMLElement;
  const submitBtn = document.getElementById("prompt-submit") as HTMLButtonElement;
  const cancel = document.getElementById("prompt-cancel") as HTMLButtonElement;
  (document.getElementById("prompt-title") as HTMLElement).textContent = cfg.title;
  (document.getElementById("prompt-intro") as HTMLElement).textContent = cfg.intro;
  submitBtn.textContent = cfg.submitLabel;
  error.hidden = true;
  const inputs: (HTMLInputElement | HTMLSelectElement)[] = cfg.fields.map((f) => f.type === "select"
    ? h("select", { ariaLabel: f.label }, ...(f.options ?? []).map(([v, l]) => h("option", { value: v }, l)))
    : h("input", { type: f.type ?? "text", autocomplete: "off", required: !f.optional && !cfg.primary, placeholder: f.placeholder, ariaLabel: f.label }));
  const primaryBtn = cfg.primary ? h("button", { type: "button", id: "prompt-primary" }, cfg.primary.label) : null;
  box.replaceChildren(
    ...(primaryBtn ? [primaryBtn] : []),
    ...cfg.fields.map((f, i) => h("label", { class: "field" }, h("span", {}, f.label), inputs[i])),
    ...(cfg.footnote ? [h("p", { class: "muted" }, cfg.footnote)] : []));
  dialog.hidden = false;
  (primaryBtn ?? inputs[0])?.focus();
  return new Promise((resolve) => {
    const finish = (ok: boolean) => {
      dialog.hidden = true;
      form.removeEventListener("submit", onSubmit);
      primaryBtn?.removeEventListener("click", onPrimary);
      cancel.removeEventListener("click", onCancel);
      dialog.removeEventListener("keydown", onKey);
      dialog.removeEventListener("mousedown", onBackdrop);
      resolve(ok);
    };
    const attempt = async (action: (values: string[]) => Promise<void>) => {
      error.hidden = true;
      submitBtn.disabled = true;
      if (primaryBtn) primaryBtn.disabled = true;
      try {
        await action(inputs.map((i) => i.value.trim()));
        finish(true);
      } catch (e) {
        error.textContent = errorMessage(e);
        error.hidden = false;
      } finally {
        submitBtn.disabled = false;
        if (primaryBtn) primaryBtn.disabled = false;
      }
    };
    const onSubmit = (ev: Event) => {
      ev.preventDefault();
      void attempt(cfg.run);
    };
    const onPrimary = () => void attempt(cfg.primary!.run);
    primaryBtn?.addEventListener("click", onPrimary);
    const onCancel = () => finish(false);
    const onBackdrop = (ev: Event) => ev.target === dialog && finish(false);
    const onKey = (ev: KeyboardEvent) => {
      if (ev.key === "Escape") return finish(false);
      if (ev.key !== "Tab") return;
      const items = [...dialog.querySelectorAll<HTMLElement>("input, select, button:not([disabled])")];
      const first = items[0];
      const last = items[items.length - 1];
      if (ev.shiftKey && document.activeElement === first) (ev.preventDefault(), last.focus());
      else if (!ev.shiftKey && document.activeElement === last) (ev.preventDefault(), first.focus());
    };
    form.addEventListener("submit", onSubmit);
    cancel.addEventListener("click", onCancel);
    dialog.addEventListener("keydown", onKey);
    dialog.addEventListener("mousedown", onBackdrop);
  });
}
