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

type Route = Response | Error | object | ((init: RequestInit | undefined) => Response | Error | Promise<Response>);

// fetchRoutes answers each request by its longest matching "METHOD /path" prefix (GET when no
// method is given): a plain object is a 200 JSON body; a Response or an Error is used once as is;
// a function builds the response (or a promise of one, to hold a request open) or an error.
export function fetchRoutes(routes: Record<string, Route>) {
  const mock = vi.fn<typeof fetch>(async (input, init) => {
    const request = `${init?.method ?? 'GET'} ${String(input)}`;
    const normal = (route: string) => (route.includes(' ') ? route : `GET ${route}`);
    const key = Object.keys(routes)
      .filter((route) => request.startsWith(normal(route)))
      .sort((a, b) => normal(b).length - normal(a).length)[0];
    if (key === undefined) return jsonResponse(404, { error: { code: 'NOT_FOUND', message: `No test route for ${request}.` } });
    const route = routes[key];
    const response = typeof route === 'function' ? await route(init) : route instanceof Response || route instanceof Error ? route : jsonResponse(200, route);
    if (response instanceof Error) throw response;
    return response;
  });
  vi.stubGlobal('fetch', mock);
  return mock;
}
