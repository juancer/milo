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
