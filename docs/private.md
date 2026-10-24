# Private apps

A private app is not on the internet. It is for a server that you reach through a VPN, like Tailscale or WireGuard: an admin tool, the logs of your servers, a dashboard of your team. It has no domain and no route on the proxy. Its port listens on an address of the server that only the server and the machines of your VPN reach. Everything else of an app stays: the versions, `chasen rollback`, the backups, and the live replica.

```bash
chasen deploy --private                      # 127.0.0.1 of the server
chasen deploy --private=10.8.0.1             # the address of the server in your VPN
chasen deploy ghcr.io/acme/admin --private   # an image that another repository releases
```

The app listens at the port of the app: 8080, or the next port that no other private app has. The deploy says the address:

```
Deployed admin 1.4.0
  http://127.0.0.1:8080 on the server, not on the internet
```

`chasen status` says it too: `Private:  127.0.0.1:8080, not on the internet`.

## Reach it through your VPN

### Tailscale

Deploy with `--private`. Then, on the server:

```bash
tailscale serve --bg 8080
```

The machines of your tailnet open `https://<server>.<tailnet>.ts.net`, with a certificate from Tailscale. Nothing listens on the internet: `tailscale serve` answers only in your tailnet.

### WireGuard, NetBird, ZeroTier, or another VPN

Deploy with the address of the server in the VPN. `ip -brief address` on the server shows it, next to the interface of the VPN, like `wg0`:

```bash
chasen deploy --private=10.8.0.1
```

The machines of the VPN open `http://10.8.0.1:8080`. A port can follow the address: `--private=10.8.0.1:3001`.

Docker must start after the VPN. When the address is not there yet at boot, the app can stay down until `chasen restart`. For WireGuard, run `systemctl edit docker` on the server, and add:

```ini
[Unit]
After=wg-quick@wg0.service
Wants=wg-quick@wg0.service
```

`127.0.0.1` is always there, so Tailscale needs none of this.

### No VPN: an SSH tunnel

From your computer:

```bash
ssh -L 8080:127.0.0.1:8080 root@203.0.113.5
```

Then open `http://localhost:8080`. The app is there while the tunnel is open.

## In chasen.yml

`private:` is one line, with the values of the flag:

```yaml
private: true            # 127.0.0.1
private: 10.8.0.1        # the address of the server in the VPN
private: 10.8.0.1:3001   # with a port
```

- **The flag and the file say one thing.** When `--private` and `private:` name two addresses, the deploy stops before the build.
- **`chasen restart` applies a new address** of `chasen.yml` too. A restart never puts an app on the internet.
- **`private: false` is not a setting.** The deploy stops and says how to put the app on the internet.

## Put it on the internet

```bash
chasen deploy --domain admin.example.com
```

Only a domain puts a private app on the internet. Remove `private:` from `chasen.yml` first, or the deploy stops. Then the app has a domain and a certificate like any other app, and its address on the server is free again.

A deploy that says nothing keeps a private app private: also after you remove `private:` from `chasen.yml`, and also the nightly update of `--auto-update`. So an app does not go on the internet by mistake.

An app on the internet becomes private with `chasen deploy --private`. Its domains leave the proxy.

## What to know

- **Only an address that the internet does not reach.** `127.0.0.1`, the private ranges (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, and `fc00::/7`), and `100.64.0.0/10`, the range of Tailscale and NetBird. A public address and `0.0.0.0` are refused.
- **A deploy stops the app for a few seconds.** Two containers cannot have one port, so the old one stops, then the new one starts. When the new one does not answer on `/up`, the old one starts again. An app on the internet has no such gap: the proxy moves the traffic from the old container to the new one.
- **Private is not a login.** Every machine of the VPN reaches the app. Keep the login of the app on.
- **`BASE_URL` is the address on the server**, like `http://127.0.0.1:8080`. See [the app standard](../STANDARD.md#4-environment).
- **A private app has no domains.** `chasen domains add` refuses it, and points to `chasen deploy --domain`.
- **It needs chasen-server v0.13.0 or newer.** The CLI asks the server for its version before a private deploy. A server from before does not know the setting: it would put the app on the internet. `chasen-server update` on the server updates it.
