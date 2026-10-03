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

### Let only Cloudflare in

With the proxy on for every name of the server, ports 80 and 443 need to take traffic from Cloudflare only. With ufw:

```bash
for ip in $(curl -fsSL https://www.cloudflare.com/ips-v4) $(curl -fsSL https://www.cloudflare.com/ips-v6); do
  ufw allow from $ip to any port 80,443 proto tcp
done
ufw delete allow 80,443/tcp   # the rule that let everyone in, when you had one
```

Every name of the server must then go through Cloudflare, also a new one: a record with the proxy off no longer reaches the server, and its certificate cannot come. Cloudflare changes its list of addresses rarely; run the loop again when it does.
