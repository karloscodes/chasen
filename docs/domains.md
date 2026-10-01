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
