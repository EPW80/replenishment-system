/*
 * The two reads screen 1 projects from. Nothing else is fetched here, and nothing here
 * writes: the transitions are the next screen's work.
 *
 * Calls go to the same origin as the page. In production that origin is WordPress: spec
 * §4 puts a thin mu-plugin in front of CadenceOS that exchanges a WP nonce for a JWT
 * and proxies authenticated calls. Locally cmd/portaldev stands in for it. Either way
 * the browser never talks to CadenceOS cross-origin and never holds the service
 * credential, which is why the Go service carries no CORS middleware.
 */

/** An API failure with the status attached, so a caller can tell 401 from 409 from 500
 *  without parsing strings. */
export class ApiError extends Error {
  constructor(status, message, options) {
    super(message, options);
    this.name = 'ApiError';
    this.status = status;
  }
}

async function get(path, { signal } = {}) {
  let res;
  try {
    res = await fetch(path, { signal, headers: { Accept: 'application/json' } });
  } catch (cause) {
    // A dropped connection is indistinguishable from an offline browser here, and both
    // want the same retry affordance as a 500.
    throw new ApiError(0, 'Could not reach the server.', { cause });
  }

  if (!res.ok) {
    // Every handler answers {"error": "..."} (internal/httpapi writeError). A body that
    // is not that shape means something other than the API answered -- a proxy, most
    // likely -- so fall back to the status rather than rendering its HTML.
    let message = `Request failed (${res.status}).`;
    try {
      const body = await res.json();
      if (typeof body?.error === 'string') message = body.error;
    } catch {
      /* keep the fallback */
    }
    throw new ApiError(res.status, message);
  }

  return res.json();
}

export function getSchedule(scheduleID, options) {
  return get(`/api/schedules/${encodeURIComponent(scheduleID)}`, options);
}

export async function getOccurrences(scheduleID, options) {
  const body = await get(
    `/api/schedules/${encodeURIComponent(scheduleID)}/occurrences`,
    options,
  );
  return body.occurrences ?? [];
}
