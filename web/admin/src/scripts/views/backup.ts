import { actions, badge, button, card, checkbox, field, fields, formCard, list, note, row, submit, toggle } from "../../components/ui";
import { t } from "../../i18n";
import { api, apiFile, apiUpload } from "../api";
import { clear, errorMessage, h, when } from "../dom";
import { guard, toast } from "../ui";

interface State {
  settings: { auto: boolean; dir: string; keep: number; every_hours: number; last_at?: string; last_error?: string };
  files: { name: string; size: number; at: string }[];
  min_passphrase: number;
}

function size(bytes: number): string {
  return bytes > 1 << 20 ? `${(bytes / (1 << 20)).toFixed(1)} MB` : `${Math.max(1, Math.round(bytes / 1024))} KB`;
}

export async function render(root: HTMLElement): Promise<void> {
  const st = await api<State>("GET", "/api/backup");
  clear(root);
  const refresh = () => render(root);

  // ---- download
  const pass = h("input", { type: "password", autocomplete: "new-password", minLength: st.min_passphrase, required: true, ariaLabel: t("backup.passphrase") });
  const key = checkbox(true);
  root.append(formCard(async (ev) => {
    ev.preventDefault();
    await guard(async () => {
      const { blob, filename } = await apiFile("POST", "/api/backup/download", { passphrase: pass.value, include_key: key.checked });
      const url = URL.createObjectURL(blob);
      const link = h("a", { href: url, download: filename });
      document.body.append(link);
      link.click();
      link.remove();
      setTimeout(() => URL.revokeObjectURL(url), 10000);
      pass.value = "";
    }, "backup.downloaded");
  },
    h("h2", {}, t("backup.download")),
    note(t("backup.downloadHelp")),
    fields(field(t("backup.passphraseLabel", { min: st.min_passphrase }), pass),
      toggle(t("backup.includeKey"), key),
      submit(t("backup.downloadButton")))));

  // ---- restore
  const file = h("input", { type: "file", accept: ".milo", required: true, ariaLabel: t("backup.file") });
  const restorePass = h("input", { type: "password", autocomplete: "off", required: true, ariaLabel: t("backup.restorePassphrase") });
  root.append(formCard(async (ev) => {
    ev.preventDefault();
    const f = file.files?.[0];
    if (!f) return;
    const form = new FormData();
    form.append("passphrase", restorePass.value); // must come before the file
    form.append("file", f);
    try {
      const res = await apiUpload<{ device_name: string; created_at: string }>("/api/backup/restore", form);
      restorePass.value = "";
      clear(root);
      root.append(card(t("backup.ready"),
        h("p", {}, t("backup.readyHelp", { name: res.device_name, when: when(res.created_at) })),
        actions(button(t("backup.restartNow"), { onclick: () => guard(() => api("POST", "/api/system/restart"), "backup.restarting") }))));
    } catch (e) {
      toast(errorMessage(e), true);
    }
  },
    h("h2", {}, t("backup.restore")),
    note(t("backup.restoreHelp")),
    fields(field(t("backup.file"), file), field(t("backup.passphraseShort"), restorePass), submit(t("backup.prepare"), "danger"))));

  // ---- automatic copies
  const s = st.settings;
  const auto = checkbox(s.auto);
  const dir = h("input", { type: "text", value: s.dir, ariaLabel: t("backup.folder") });
  const keep = h("input", { type: "number", value: String(s.keep), min: "1", max: "60", ariaLabel: t("backup.keepCopies") });
  const every = h("input", { type: "number", value: String(s.every_hours), min: "1", max: "168", ariaLabel: t("backup.everyHours") });
  root.append(formCard(async (ev) => {
    ev.preventDefault();
    if (await guard(() => api("PUT", "/api/backup", { auto: auto.checked, dir: dir.value.trim(), keep: Number(keep.value), every_hours: Number(every.value) }), "common.saved")) await refresh();
  },
    h("h2", {}, t("backup.automatic")),
    note(t("backup.automaticHelp")),
    s.last_error ? h("p", { class: "error" }, t("backup.lastFailed", { error: s.last_error })) : null,
    h("p", {}, s.last_at ? badge(t("backup.lastCopy", { when: when(s.last_at) })) : badge(t("backup.noCopy"), true)),
    fields(toggle(t("backup.auto"), auto), field(t("backup.every"), every), field(t("backup.keep"), keep), field(t("backup.folder"), dir)),
    list(...st.files.map((f) => row(h("span", {}, f.name), h("span", { class: "muted" }, `${size(f.size)} · ${when(f.at)}`)))),
    actions(submit(t("common.save")),
      button(t("backup.copyNow"), { variant: "secondary", onclick: async () => {
        if (await guard(() => api("POST", "/api/backup/run"), "backup.copyMade")) await refresh();
      } }))));
}
