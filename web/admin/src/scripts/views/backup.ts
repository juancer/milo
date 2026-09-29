import { api, apiFile, apiUpload } from "../api";
import { badge, clear, errorMessage, field, h, toast, when } from "../dom";

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
  const note = (t: string) => h("p", { class: "muted" }, t);

  // ---- download
  const pass = h("input", { type: "password", autocomplete: "new-password", minLength: st.min_passphrase, required: true, ariaLabel: "Backup passphrase" });
  const key = h("input", { type: "checkbox", checked: true });
  root.append(h("form", {
    class: "card",
    onsubmit: async (ev: Event) => {
      ev.preventDefault();
      try {
        const { blob, filename } = await apiFile("POST", "/api/backup/download", { passphrase: pass.value, include_key: key.checked });
        const url = URL.createObjectURL(blob);
        const link = h("a", { href: url, download: filename });
        document.body.append(link);
        link.click();
        link.remove();
        setTimeout(() => URL.revokeObjectURL(url), 10000);
        pass.value = "";
        toast("Backup downloaded. Keep the file and the passphrase somewhere safe.");
      } catch (e) {
        toast(errorMessage(e), true);
      }
    },
  },
    h("h2", {}, "Download a backup"),
    note("One encrypted file with everything: settings, family, messages, conversations and the encrypted secrets. Keep it on a USB stick or in your cloud storage - without the passphrase nobody can read it. A memory card can fail without warning, so make a copy now and after big changes."),
    h("div", { class: "fields" }, field(`Passphrase (at least ${st.min_passphrase} characters - write it down!)`, pass),
      h("label", { class: "check" }, key, " Include the master key (needed to rebuild the device on a new card)"),
      h("button", { type: "submit" }, "Download backup"))));

  // ---- restore
  const file = h("input", { type: "file", accept: ".nexo", required: true, ariaLabel: "Backup file" });
  const restorePass = h("input", { type: "password", autocomplete: "off", required: true, ariaLabel: "Backup passphrase for restore" });
  root.append(h("form", {
    class: "card",
    onsubmit: async (ev: Event) => {
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
        root.append(h("section", { class: "card" }, h("h2", {}, "Backup checked and ready"),
          h("p", {}, `“${res.device_name}”, made ${when(res.created_at)}. It replaces everything on this device (settings, family, messages and administrator accounts) when Nexo restarts. The current data is kept aside on the device.`),
          h("div", { class: "actions" }, h("button", { type: "button", onclick: async () => {
            try { await api("POST", "/api/system/restart"); toast("Restarting… sign in again in a few seconds."); } catch (e2) { toast(errorMessage(e2), true); }
          } }, "Restart now to restore"))));
      } catch (e) {
        toast(errorMessage(e), true);
      }
    },
  },
    h("h2", {}, "Restore from a backup"),
    note("Choose a backup file and enter its passphrase. Nexo checks it first and changes nothing until you restart. This works for backups made by this device. To move a backup to a new device or a new memory card, use the command line: sudo systemctl stop nexo; sudo nexo restore FILE --replace-key"),
    h("div", { class: "fields" }, field("Backup file", file), field("Passphrase", restorePass), h("button", { type: "submit", class: "danger" }, "Check and prepare restore"))));

  // ---- automatic copies
  const s = st.settings;
  const auto = h("input", { type: "checkbox", checked: s.auto });
  const dir = h("input", { type: "text", value: s.dir, ariaLabel: "Folder" });
  const keep = h("input", { type: "number", value: String(s.keep), min: "1", max: "60", ariaLabel: "Copies to keep" });
  const every = h("input", { type: "number", value: String(s.every_hours), min: "1", max: "168", ariaLabel: "Every hours" });
  root.append(h("form", {
    class: "card",
    onsubmit: async (ev: Event) => {
      ev.preventDefault();
      try {
        await api("PUT", "/api/backup", { auto: auto.checked, dir: dir.value.trim(), keep: Number(keep.value), every_hours: Number(every.value) });
        toast("Saved");
        await refresh();
      } catch (e) {
        toast(errorMessage(e), true);
      }
    },
  },
    h("h2", {}, "Automatic copies on the device"),
    note("Nexo keeps a few recent copies of its database so a damaged file can be recovered. They live on the same memory card, so they do not replace a downloaded backup. They are not encrypted (they contain what the database contains) and never contain the master key."),
    s.last_error ? h("p", { class: "error" }, `Last attempt failed: ${s.last_error}`) : null,
    h("p", {}, s.last_at ? badge(`last copy ${when(s.last_at)}`) : badge("no copy yet", true)),
    h("div", { class: "fields" }, h("label", { class: "check" }, auto, " Make copies automatically"), field("Every (hours)", every), field("Copies to keep", keep), field("Folder", dir)),
    h("ul", { class: "list" }, ...st.files.map((f) => h("li", { class: "row" }, h("span", {}, f.name), h("span", { class: "muted" }, `${size(f.size)} · ${when(f.at)}`)))),
    h("div", { class: "actions" }, h("button", { type: "submit" }, "Save"),
      h("button", { type: "button", class: "secondary", onclick: async () => {
        try { await api("POST", "/api/backup/run"); toast("Copy made"); await refresh(); } catch (e) { toast(errorMessage(e), true); }
      } }, "Make a copy now"))));
}
