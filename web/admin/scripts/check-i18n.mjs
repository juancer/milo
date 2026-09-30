// Checks the translation catalogs. Run inside Docker: `pnpm run check:i18n`.
//  1. every key of every locale exists in en.json (the source language)
//  2. {placeholders} match the English text
//  3. every literal t("key") used in src exists in en.json
// Keys missing from a non-English locale are only reported (they fall back to English).
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

const root = new URL("../src/", import.meta.url).pathname;
const dir = join(root, "i18n/locales");
const load = (f) => JSON.parse(readFileSync(join(dir, f), "utf8"));
const en = load("en.json");
const holders = (s) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort().join(",");
const namespaces = new Set(Object.keys(en).map((k) => k.split(".")[0]));
let failed = false;
const fail = (msg) => {
  failed = true;
  console.error("FAIL " + msg);
};

for (const file of readdirSync(dir).filter((f) => f.endsWith(".json") && f !== "en.json")) {
  const cat = load(file);
  const missing = Object.keys(en).filter((k) => !(k in cat));
  for (const k of Object.keys(cat)) {
    if (!(k in en)) fail(`${file}: key "${k}" is not in en.json`);
    else if (holders(cat[k]) !== holders(en[k])) fail(`${file}: "${k}" placeholders differ from English`);
  }
  if (missing.length) console.warn(`note ${file}: ${missing.length} untranslated key(s) fall back to English: ${missing.slice(0, 5).join(", ")}${missing.length > 5 ? ", …" : ""}`);
}

function* walk(d) {
  for (const name of readdirSync(d)) {
    const p = join(d, name);
    if (statSync(p).isDirectory()) yield* walk(p);
    else if (/\.(ts|astro)$/.test(name)) yield p;
  }
}
for (const file of walk(root)) {
  const src = readFileSync(file, "utf8");
  for (const m of src.matchAll(/\b(?:t|tOr)\(\s*["']([a-z][\w.]*)["']/g)) if (!(m[1] in en)) fail(`${file}: unknown key "${m[1]}"`);
  // any "namespace.key" literal whose namespace is a translation namespace (covers guard(fn, "common.saved"))
  for (const m of src.matchAll(/"([a-z][A-Za-z]*)\.([A-Za-z0-9_.]+)"/g)) if (namespaces.has(m[1]) && !(m[0].slice(1, -1) in en)) fail(`${file}: unknown key ${m[0]}`);
  for (const m of src.matchAll(/data-i18n(?:-[a-z-]+)?=["']([a-z][\w.]*)["']/g)) if (!(m[1] in en)) fail(`${file}: unknown key "${m[1]}"`);
}
console.log(failed ? "i18n check failed" : `i18n ok (${Object.keys(en).length} keys)`);
process.exit(failed ? 1 : 0);
