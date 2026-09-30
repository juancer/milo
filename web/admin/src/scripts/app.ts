/**
 * Single entry point of the admin UI (imported once by layouts/AdminLayout.astro).
 * Shared behaviour lives in modules next to this file:
 *   api.ts      HTTP client        dom.ts      element builder + formatting
 *   ui.ts       toasts, confirmations, locale switch     locales.ts  language options
 *   shell.ts    sign-in, navigation, routing             views/*     one module per section
 * Reusable markup builders live in ../components/ui.ts and text in ../i18n.
 */
import { start } from "./shell";

void start();
