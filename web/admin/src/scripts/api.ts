export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

/** JSON request helper. The custom header is required by the server's CSRF guard. */
export async function api<T = void>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: { "Content-Type": "application/json", "X-Requested-With": "nexo-admin" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  const data = text ? JSON.parse(text) : undefined;
  if (!res.ok) throw new ApiError(res.status, data?.error ?? res.statusText);
  return data as T;
}

export interface Status {
  version: string;
  uptime_seconds: number;
  hardware: string;
  schema_version: number;
  go_version: string;
  arch: string;
  audio: { backend: string; available: boolean; volume: number; detail?: string };
  hardware_errors: string[] | null;
}

/** POST/GET that returns a file (used for backup downloads). */
export async function apiFile(method: string, path: string, body?: unknown): Promise<{ blob: Blob; filename: string }> {
  const res = await fetch(path, {
    method,
    headers: { "Content-Type": "application/json", "X-Requested-With": "nexo-admin" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    const text = await res.text();
    let message = res.statusText;
    try {
      message = JSON.parse(text).error ?? message;
    } catch {
      /* not JSON */
    }
    throw new ApiError(res.status, message);
  }
  const match = /filename="([^"]+)"/.exec(res.headers.get("Content-Disposition") ?? "");
  return { blob: await res.blob(), filename: match?.[1] ?? "nexo-backup.nexo" };
}

/** Uploads a form (fields first, then the file) and returns the JSON answer. */
export async function apiUpload<T>(path: string, form: FormData): Promise<T> {
  const res = await fetch(path, { method: "POST", headers: { "X-Requested-With": "nexo-admin" }, body: form });
  const text = await res.text();
  const data = text ? JSON.parse(text) : undefined;
  if (!res.ok) throw new ApiError(res.status, data?.error ?? res.statusText);
  return data as T;
}
