# Domains

```bash
chasen domains add myapp.com
chasen domains add www.myapp.com
chasen domains            # list
chasen domains rm www.myapp.com
```

Point an A record for the domain to the server. The certificate comes on the first HTTPS request after DNS resolves. The change needs no build and has no downtime.

## Cloudflare in front (recommended, optional)

Put Cloudflare in front of your domains: turn the proxy on and set the SSL mode to **Full (strict)**. Chasen needs no setting for it.

- Visitors see Cloudflare's addresses, not the address of your server.
- Cloudflare caches images, scripts, and styles at its edge. Most requests of a traffic spike never reach the server. Pages that the app renders still come from the server, unless you add a cache rule.
- Chasen keeps its own Let's Encrypt certificate, so the connection stays encrypted from the visitor to the app.

## Hold requests while the server reboots (optional)

A deploy and a restart drop no request: the proxy on the server keeps the old version until the new one answers. A reboot is different. The whole server is away for about a minute, and the proxy with it, so nothing on the server can hold a request. The request must wait in front of the server.

With Cloudflare in front, a small Worker does that. While the server is away, it keeps the request of the visitor open and tries again every 3 seconds, for up to 90 seconds. The visitor sees a slow page, not an error.

```js
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
```

To install it:

1. In the Cloudflare dashboard, open **Workers & Pages**, create a Worker, and paste the code.
2. Open the Worker, then **Settings**, then **Domains & Routes**. Add a route for each domain of your apps: `shop.example.com/*`, or `*.example.com/*` for all of them.
3. Open your site. It works as before. To see the Worker hold a request, stop the proxy for a moment on the server: `docker stop matcha-proxy; sleep 20; docker start matcha-proxy`.

What it does not do:

- It tries again only when the server did not get the request, so no request runs twice. A request that was in the middle of its work when the reboot started can still fail.
- It cannot hold a WebSocket or an upload larger than 10 MB.
- Every request goes through the Worker. The free plan of Cloudflare has 100,000 requests each day.

Without Cloudflare there is no place in front of the server, and a reboot costs about one minute of errors.
