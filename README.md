# uProxy

A lightweight, configurable **TCP / UDP / HTTPS reverse proxy** written in Go, built for
Minecraft: **Java** (TCP) and **Bedrock** (RakNet and **NetherNet**) servers behind
[Geyser](https://geysermc.org), Velocity/Paper, or Bedrock Dedicated Server.

uProxy forwards traffic and tells the backend who the real player is using the
**HAProxy PROXY protocol v2**, so bans, whitelists and logs keep working behind the proxy.

## Features

- **Bedrock endpoint in one entry** (`protocol: "bedrock"`): TCP + UDP on the same port.
  TCP carries the NetherNet HTTP signaling (`GET /v1/join`, `POST /v1/join/{networkId}`),
  UDP carries RakNet and/or WebRTC gameplay.
- **Per-flow PROXY v2 policy for UDP** that follows the conventions Geyser (CloudburstMC Network) implements:
  - `raknet`: header on the **first datagram** of a flow, re-sent after the upstream direction was idle ≥ 10 s
    (the backend forgets the mapping after 10 s).
  - `nethernet`: header on **every datagram** (Geyser `webrtc-proxy-protocol-relays`).
  - `plain`: no header (Bedrock Dedicated Server).
  - `auto` (default): STUN/DTLS ⇒ `nethernet`, everything else ⇒ `raknet`.
- **One upstream source port per client**, which is what both conventions require to route replies.
- TCP proxy with PROXY v2 (IPv4 and IPv6), half-close aware, DNS backends with re-resolution.
- RakNet unconnected pong port rewriting (server list shows the proxy's port).
- SNI-routed HTTPS reverse proxy (many domains per port) with `X-Forwarded-For`, `X-Forwarded-Proto`, `X-Real-IP`.
- Idle flow reaping, flow limits, graceful shutdown on SIGINT/SIGTERM, debug logging.

## Install

Download a build from the releases/actions page, or build it yourself:

```bash
git clone https://github.com/AvionBlock/uProxy.git
cd uProxy
go build -trimpath -ldflags "-s -w" -o uproxy .
./uproxy                     # creates config.json interactively when missing
./uproxy -config /etc/uproxy/config.json -debug
```

Go 1.24+ is required. The environment variable `UPROXY_CONFIG` also sets the config path.

## Configuration

See [`config.example.json`](config.example.json). Every L4 entry under `proxies`:

| Key | Default | Meaning |
|---|---|---|
| `name` | – | Label used in logs. |
| `listenIP` | `0.0.0.0` | Local bind address (`::` for dual-stack). |
| `listenPort` | – | Local port; for `bedrock` used for both TCP and UDP. |
| `serverHost` | – | Backend IP or hostname (re-resolved every minute). |
| `serverPort` | – | Backend port. |
| `protocol` | legacy `protoTCP` | `tcp`, `udp` or `bedrock` (TCP + UDP). |
| `proxyProtocol` | `true` | Send PROXY protocol v2 headers to the backend. |
| `udpMode` | `auto` | `auto`, `raknet`, `nethernet`, `plain` (see above). |
| `rewritePongPort` | `true` | Rewrite RakNet pong port fields to `listenPort` when it differs from `serverPort`. |
| `idleTimeout` | `60s` | Close a UDP flow after this much silence in both directions. |
| `raknetHeaderResendIdle` | `10s` | Re-send the header on a RakNet flow idle for this long upstream. |
| `upstreamBindIP` | `listenIP` if specific | Source address used towards the backend (what Geyser whitelists). |
| `maxUDPFlows` | `10000` | Cap of concurrent UDP flows per listener. |
| `tcpDialTimeout` | `10s` | Backend connect timeout. |

Old config files (`protoTCP: true/false`) keep working unchanged.

Entries under `reverseProxies`: `listenPort`, `certFile`, `keyFile`, `serverHost` (`host:port`),
`serverProto` (`http`/`https`), optional `listenIP`, `domain` (defaults to the certificate's DNS name),
`preserveHost`, `insecureSkipVerify`.

## Geyser setup

Since Bedrock **26.50** NetherNet is the default transport and since **26.60** (October 2026) clients no
longer connect over RakNet. A NetherNet join is two steps: an HTTP request over **TCP** to the server
port (signaling, returns an SDP answer) and then WebRTC (ICE/DTLS/SCTP) over **UDP**. Geyser serves both on
`bedrock.port` (TCP and UDP) by default; `bedrock.webrtc-port` moves the UDP side.

### 1. Geyser with NetherNet (recommended)

uProxy `config.json`:

```json
{
  "proxies": [{
    "name": "geyser", "listenIP": "0.0.0.0", "listenPort": 19132,
    "serverHost": "10.0.0.2", "serverPort": 19132,
    "protocol": "bedrock", "proxyProtocol": true, "udpMode": "auto"
  }]
}
```

Geyser `config.yml` (build 1272 or newer):

```yaml
bedrock:
  port: 19132
  transport: nethernet          # or "both" while RakNet players still exist
  signaling:
    mode: builtin
    stun-discovery: false       # must be off when relays are in front of the media port
    advertise-addresses:
      - 203.0.113.5:19132       # uProxy's PUBLIC ip:port (UDP) - this is what players connect to
advanced:
  bedrock:
    haproxy-protocol-whitelisted-ips:
      - 10.0.0.1                # uProxy's upstream address: TCP signaling (and RakNet) PROXY headers
    webrtc-proxy-protocol-relays:
      - 10.0.0.1                # same address: PROXY v2 header on every WebRTC datagram
```

Notes:

- `advertise-addresses` has to name uProxy, not Geyser: with relays configured Geyser never contacts the
  player directly and only answers through the relay the player came through.
- Listing any address in `haproxy-protocol-whitelisted-ips` makes Geyser **require** PROXY headers from
  it, on TCP signaling and on RakNet. Keep `proxyProtocol: true` in uProxy.
- Alternatively, forward UDP without headers (`udpMode: "plain"`) and set
  `advanced.bedrock.prefer-signaling-proxy-address: true`; Geyser then uses the address learned from the
  signaling PROXY header for the session.
- The Bedrock client also probes `https://host` and `http://host` (ports 443/80) when the port is 19132 or
  omitted. The reverse proxy below can serve signaling on 443 with a real certificate, which avoids the
  trust-on-first-use prompt. Add uProxy to `advanced.bedrock.signaling-trusted-proxies` in that case
  (it forwards `X-Forwarded-For`).

### 2. Geyser with `transport: both`

RakNet and WebRTC need separate UDP ports in Geyser (`port` vs `webrtc-port`). Add one more entry:

```json
{ "name": "geyser-raknet", "listenPort": 19133, "serverHost": "10.0.0.2", "serverPort": 19133,
  "protocol": "udp", "udpMode": "raknet" }
```

and point `bedrock.webrtc-port` / `-DgeyserWebrtcPort` at the UDP port of the `bedrock` entry.

### 3. Bedrock Dedicated Server

BDS does not understand PROXY protocol. Use `protocol: "bedrock"` with `proxyProtocol: false`. Set
`server-udp-ports` in `server.properties` to a fixed port and forward that port as well if it differs
from `server-port`.

### 4. Minecraft Java

`protocol: "tcp"` with `proxyProtocol: true`; enable `haproxy-protocol` in Velocity/BungeeCord or
`proxy-protocol` in Paper.

## How the UDP side works

Each client `ip:port` gets its own **flow** with a dedicated UDP socket towards the backend, so the backend sees a
unique source port per player and replies to it. uProxy maps that socket back to the player. The first
datagram decides the flow type:

| First byte | Meaning | Flow |
|---|---|---|
| `0x00..0x03` + STUN cookie `21 12 a4 42` | ICE connectivity check | `nethernet` |
| `20..63` + DTLS version `fe fd` / `fe ff` | DTLS record | `nethernet` |
| `0x01`, `0x02`, `0x05`, `0x07` + RakNet magic | Unconnected ping / open connection request | `raknet` |
| `0x80..0xff` | RakNet datagram / ACK / NAK | `raknet` |
| anything else | – | `raknet` (header on first datagram) |

In `raknet` mode the backend (CloudburstMC Network `RakProxyServerHandler`) parses the header only on the
first datagram from a relay port and remembers the player for 10 s after the last datagram, so uProxy
re-sends the header whenever the upstream direction was idle for `raknetHeaderResendIdle`. In `nethernet`
mode the native ICE multiplexer expects the header on every datagram, so every datagram carries it.

## Reverse HTTPS proxy

```json
"reverseProxies": [{
  "listenPort": 443, "domain": "play.example.com",
  "certFile": "/etc/letsencrypt/live/play.example.com/fullchain.pem",
  "keyFile":  "/etc/letsencrypt/live/play.example.com/privkey.pem",
  "serverHost": "10.0.0.2:19132", "serverProto": "http"
}]
```

Multiple domains can share one listen port; the certificate is selected by SNI.

## License

See [LICENSE](LICENSE).
