# Caddy production deployment

This deployment exposes one Slider process through two HTTPS hostnames:

```text
agents -> DATA_DOMAIN    -> Caddy -> HTTPS -> Slider :8080
admins -> CONTROL_DOMAIN -> Caddy -> HTTPS -> Slider :8080
```

The data hostname accepts only Slider WebSocket handshakes on `/`. The control
hostname serves the Slider web console, rejects Slider data handshakes, and can
optionally be restricted by source CIDR. `/health` and `/version` remain
private to the Docker network.

This is ingress isolation, not process isolation. Control and data traffic
still terminate in the same Slider process and share its failure domain.

## Prerequisites

- Docker Engine with the Compose plugin
- Two DNS names pointing to the deployment host
- TCP ports 80 and 443 open for Caddy and ACME
- UDP port 443 open when HTTP/3 is wanted

## First deployment

Run deployment commands from the repository root. Create the local environment
file and replace every example value:

```sh
make deploy-init
$EDITOR deploy/.env
```

The control hostname permits all IPv4 and IPv6 sources by default. To restrict
it, set one or more space-separated CIDRs:

```dotenv
CONTROL_ALLOWED_CIDRS=198.51.100.24/32 2001:db8:1234::/48
```

The control console is served from `CONTROL_DOMAIN` root paths by default:
`/auth`, `/console`, and `/console/ws`. To place the control console under a
path prefix, set a normalized URL path:

```dotenv
CONTROL_BASE_PATH=/aaa
```

With that setting, the external control endpoints become `/aaa/auth`,
`/aaa/console`, `/aaa/console/assets/*`, and `/aaa/console/ws`. Empty and `/`
preserve the default root paths.

Validate and start:

```sh
make deploy-config
make deploy-up
make deploy-ps
make deploy-logs
```

Caddy obtains and renews public certificates automatically. Before Slider
starts, the one-shot `certgen` service uses `step-cli` to create an internal CA
and a `DNS:slider` certificate. Slider terminates backend TLS and Caddy
verifies it against the internal CA. The CA private key is not persisted.

Slider creates its server identity and initial authorized client key in the
`slider_data` volume. The server SSH fingerprint is emitted in the Slider
startup log.

Export the initial authorized client key only to a trusted administrator host:

```sh
credential_dir="${HOME}/.config/slider"
install -d -m 0700 "$credential_dir"
docker compose --env-file deploy/.env -f deploy/compose.yaml cp \
  slider:/data/client-certs.json \
  "$credential_dir/client-certs.json"
chmod 600 "$credential_dir/client-certs.json"
jq -r 'to_entries[0].value | "fingerprint=\(.FingerPrint)\nprivate_key=\(.PrivateKey)"' \
  "$credential_dir/client-certs.json"
```

Use those two values on the control login page. An authenticated agent uses the
same private key with `--key` and connects only to the data hostname:

```sh
slider client \
  --key '<private_key>' \
  --fingerprint '<server_ssh_fingerprint>' \
  --retry \
  https://data.example.com
```

The fingerprint pins Slider's SSH identity independently of Caddy's TLS
certificate and should be retained for all clients.

## CDN deployment

The data hostname can use a CDN that supports WebSocket proxying. For
Cloudflare:

1. Proxy `DATA_DOMAIN` through Cloudflare.
2. Enable WebSockets and use `Full (strict)` origin TLS.
3. Do not enable Argo Smart Routing for this hostname.
4. Ensure Workers, Transform Rules, and WAF rules preserve
   `Upgrade`, `Sec-WebSocket-Protocol`, and `Sec-WebSocket-Operation`.
5. Keep Slider client retry enabled because CDN edge restarts and idle timeouts
   can close established WebSockets.

WebSocket payloads are not HTTP-cached. The CDN protects and filters the initial
upgrade request, but after the `101` response the encrypted Slider stream is not
inspected by HTTP WAF rules.

If `CONTROL_DOMAIN` is also proxied, `CONTROL_ALLOWED_CIDRS` may contain all
current [Cloudflare proxy ranges](https://www.cloudflare.com/ips/). This blocks
direct requests to the control origin, but it does not restrict end users:
every visitor routed through Cloudflare has a Cloudflare source address at
Caddy. Use Cloudflare Access or Cloudflare WAF rules when the control hostname
must be limited to specific users or original client IPs.

Caddy intentionally matches the direct peer address and does not trust
forwarded client-IP headers. Retrieve the current Cloudflare ranges rather than
copying a stale list:

```sh
{
  curl -fsS https://www.cloudflare.com/ips-v4
  curl -fsS https://www.cloudflare.com/ips-v6
} | paste -sd ' ' -
```

Restricting the origin firewall to Cloudflare ranges provides stronger CDN-only
enforcement than the Caddy control-host matcher alone. Cloudflare recommends
blocking non-Cloudflare sources when protecting an origin this way.

## Verification

With the default unrestricted control CIDRs:

```sh
curl -I https://control.example.com/
curl -I https://control.example.com/console
curl -i https://control.example.com/health
curl -i https://data.example.com/console
```

The expected results are redirects from the control root and console when not
authenticated, and `404` for the private health endpoint and the data hostname's
non-WebSocket console request. If `CONTROL_BASE_PATH=/aaa`, use
`https://control.example.com/aaa/console`. When `CONTROL_ALLOWED_CIDRS` is
restricted, other direct source addresses receive `403`.

Check the data handshake through a real Slider client. A plain HTTP request to
the data hostname returning `404` is intentional and is not a health failure.
The Caddy container health check independently performs TLS requests against
both configured hostnames through its loopback interface and validates their
certificates.

## Operations

Back up all persistent state:

```sh
backup_dir="${HOME}/.config/slider/backups"
install -d -m 0700 "$backup_dir"
docker run --rm \
  -v slider_slider_data:/source:ro \
  -v "$backup_dir:/backup" \
  alpine:3.22 \
  tar -czf /backup/slider-data.tar.gz -C /source .
```

The backup contains private keys. Store it with the same controls as production
credentials. Back up `slider_caddy_data` as well if preserving the current ACME
account and certificates is required. Back up `slider_slider_tls_server` and
`slider_slider_tls_trust` together to preserve the internal TLS identity.

Upgrade without deleting volumes:

```sh
make deploy-pull
make deploy-build
make deploy-up
make deploy-ps
```

Never use `docker compose down -v` unless permanent deletion of identities,
authorized client keys, and Caddy state is intended.

The internal certificate is valid for ten years. Rotate it during a maintenance
window without deleting Slider identities:

```sh
make deploy-down
docker volume rm slider_slider_tls_server slider_slider_tls_trust
make deploy-up
```

## Security boundary

- Slider has no published host port and is reachable only from Caddy.
- The backend network is marked internal.
- Caddy verifies Slider's internal TLS certificate; no application-level proxy
  bypass is enabled.
- Both containers use read-only root filesystems and drop unnecessary Linux
  capabilities.
- CIDR filtering is defense in depth. Restrict the control hostname at the
  cloud firewall or VPN layer as well.
- Caddy distinguishes the planes by hostname and WebSocket headers. Slider owns
  control path routing so `CONTROL_BASE_PATH` stays single-sourced.
  This does not provide independent scaling, resource quotas, or fault domains.
