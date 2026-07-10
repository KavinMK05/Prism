// API client - mirrors the original admin.html api() helper.
// All Go API endpoints are served under /admin/* and return JSON.

export async function api(path: string, opts?: RequestInit): Promise<any> {
  const res = await fetch('/admin' + path, opts);
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

export async function apiPost(path: string, body?: unknown): Promise<any> {
  return api(path, {
    method: 'POST',
    ...(body !== undefined && {
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  });
}

export async function apiPut(path: string, body: unknown): Promise<any> {
  return api(path, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
}
