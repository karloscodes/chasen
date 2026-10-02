// A Cloudflare Worker that holds requests while the server reboots.
//
// A reboot for a new kernel takes the server away for about a minute. Without
// this, a visitor in that minute gets an error page of Cloudflare. With it,
// the request waits at Cloudflare and goes through when the server is back:
// the visitor sees a slow page, not an error.
//
// It tries again only when the server did not get the request: the connection
// failed, or Cloudflare answered 521, 522, or 523 for it. So no request runs
// twice, a POST too. docs/domains.md says how to install it.

const WAIT = 90; // seconds before it gives up
const STEP = 3; // seconds between two tries
const LARGE = 10 * 1024 * 1024; // a larger body is not kept for a second try

export default {
  async fetch(request) {
    // A second try needs the body again, so keep it. A large upload goes
    // through one time, as it would without this Worker.
    if (Number(request.headers.get("content-length") ?? 0) > LARGE) return fetch(request);
    const body = request.body ? await request.arrayBuffer() : undefined;

    for (let waited = 0; waited < WAIT; waited += STEP) {
      try {
        const response = await fetch(new Request(request, { body }));
        // 521: the server refused. 522: it did not answer. 523: no way to it.
        if (response.status < 521 || response.status > 523) return response;
      } catch {
        // The connection failed: the server never saw the request.
      }
      await new Promise((resolve) => setTimeout(resolve, STEP * 1000));
    }
    return new Response("The server is restarting. Try again in a minute.\n", {
      status: 503,
      headers: { "retry-after": "30", "content-type": "text/plain; charset=utf-8" },
    });
  },
};
