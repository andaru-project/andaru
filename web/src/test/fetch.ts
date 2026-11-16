import { vi } from 'vitest';

export function jsonResponse(status: number, body: unknown, headers?: HeadersInit) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

export function fetchQueue(...responses: Array<Response | Error>) {
  const mock = vi.fn<typeof fetch>();
  for (const response of responses) {
    if (response instanceof Error) mock.mockRejectedValueOnce(response);
    else mock.mockResolvedValueOnce(response);
  }
  vi.stubGlobal('fetch', mock);
  return mock;
}
