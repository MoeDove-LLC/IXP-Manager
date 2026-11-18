let csrfToken = "";
let sessionContextVersion = 0;
export const setCSRF = (token: string) => {
  csrfToken = token;
  sessionContextVersion++;
};
// New transfers can discard results after any session reset, including a
// refresh that eventually returns the same CSRF token. Existing calls are unchanged.
export const captureSessionContext = () => {
  const version = sessionContextVersion;
  return () => version === sessionContextVersion;
};
export class APIError extends Error {
  constructor(
    message: string,
    public status: number,
    public data?: { mfaRequired?: boolean; retrySession?: boolean; error?: string },
  ) {
    super(message);
  }
}
export async function api<T>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const method = options.method ?? "GET";
  const requestContext = csrfToken;
  const response = await fetch(`/api${path}`, {
    ...options,
    credentials: "same-origin",
    headers: {
      Accept: "application/json",
      ...(options.body && !(options.body instanceof FormData)
        ? { "Content-Type": "application/json" }
        : {}),
      ...(method !== "GET" ? { "X-CSRF-Token": csrfToken } : {}),
      ...options.headers,
    },
  });
  const content = await response.text();
  let data: unknown;
  try {
    data = content ? JSON.parse(content) : undefined;
  } catch {
    throw new APIError(
      "服务返回了无法识别的响应，请检查 Go 服务是否正常运行。",
      response.status,
    );
  }
  if (!response.ok) {
    if (
      response.status === 401 &&
      requestContext === csrfToken &&
      path !== "/login" &&
      path !== "/session/restore" &&
      path !== "/session"
    )
      window.dispatchEvent(new Event("session-expired"));
    throw new APIError(
      (data as { error?: string })?.error ?? `请求失败 (${response.status})`,
      response.status,
      data as { mfaRequired?: boolean; retrySession?: boolean; error?: string },
    );
  }
  return data as T;
}
export const save = <T>(path: string, value: unknown, method = "POST") =>
  api<T>(path, { method, body: JSON.stringify(value) });

// A baseline is supplied by the editor that the user reviewed. Never fetch or
// replace it here: doing so would turn a stale draft into an unconditional write.
export function resourceMutation<T>(
  path: string,
  method: "PUT" | "DELETE",
  etag: unknown,
  value?: unknown,
) {
  if (typeof etag !== "string" || !/^[a-f0-9]{64}$/i.test(etag))
    return Promise.reject(
      new APIError("缺少有效的记录版本，请核对当前服务器记录后再操作。", 428),
    );
  return api<T>(path, {
    method,
    headers: { "If-Match": `"${etag}"` },
    ...(method === "PUT" ? { body: JSON.stringify(value) } : {}),
  });
}

export async function downloadFile(path: string, filename: string) {
  const requestContext = csrfToken;
  const response = await fetch(`/api${path}`, { credentials: "same-origin" });
  if (!response.ok) {
    if (response.status === 401 && requestContext === csrfToken)
      window.dispatchEvent(new Event("session-expired"));
    let message = `下载失败 (${response.status})`;
    try {
      message = (await response.json()).error ?? message;
    } catch {
      /* retain status */
    }
    throw new APIError(message, response.status);
  }
  const blob = await response.blob(),
    url = URL.createObjectURL(blob),
    link = document.createElement("a");
  link.href = url;
  link.download = filename;
  link.click();
  URL.revokeObjectURL(url);
}
