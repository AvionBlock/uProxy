# uProxy

A small TCP, UDP and HTTPS reverse proxy written in Go. TCP and UDP forward
client addresses using PROXY protocol v2. UDP forwarding is compatible with
Geyser's RakNet listener: each client gets a dedicated backend socket, with a
PROXY header on its first datagram.

## Build and run

Go 1.24.4 or newer is required.

```sh
go build -o uproxy .
./uproxy
```

On Windows, build with `go build -o uproxy.exe .` and run `uproxy.exe`.
The application reads `config.json` from its working directory. If it is
missing, an interactive setup creates it. A valid configuration needs at least
one proxy.

## Geyser UDP example

`config.json` for a public Bedrock port 19132 forwarding to Geyser on the same
machine at port 19133:

```json
{
  "debugMode": false,
  "proxies": [
    {
      "listenIP": "0.0.0.0",
      "listenPort": 19132,
      "serverHost": "127.0.0.1",
      "serverPort": 19133,
      "protoTCP": false
    }
  ],
  "reverseProxies": []
}
```

In the current Geyser configuration, enable the receiving Bedrock setting:

```yaml
advanced:
  bedrock:
    use-haproxy-protocol: true
    haproxy-protocol-whitelisted-ips: ["127.0.0.1"]
```

The whitelist must contain the IP address from which Geyser receives uProxy's
backend packets. Keep Geyser's backend port reachable only by the proxy. The
proxy's listen address and backend must use the same IP family (both IPv4 or
both IPv6). The proxy sends the PROXY v2 header with the first datagram from
each client, then forwards later datagrams unchanged. Responses are forwarded
unchanged as well. Idle UDP sessions expire after 30 seconds.

This first-datagram behavior follows Geyser's RakNet implementation. Other UDP
services that implement the current PROXY protocol specification may require a
header on every datagram and are not covered by this mode.

UDP proxies can set `udpIdleTimeoutSeconds` and `maxUDPSessions` in their
`proxies` entry. The defaults are 30 seconds and 4096 active sessions per
listener. New clients are dropped when the limit is reached; existing sessions
continue until they time out.

For TCP, set `protoTCP` to `true`. The TCP backend must accept PROXY protocol
v2 before its application data.

## HTTPS virtual hosts

Add entries to `reverseProxies` to route incoming HTTPS traffic by public
domain. Multiple domains can share a listening port:

```json
{
  "domain": "play.example.com",
  "listenPort": 443,
  "certFile": "cert.pem",
  "keyFile": "key.pem",
  "serverHost": "127.0.0.1:8080",
  "serverProto": "http"
}
```

If `domain` is omitted in an older configuration, the backend hostname is
used as the public domain. `X-Forwarded-For` and `X-Real-IP` are set from the
incoming connection address.

## Checks

```sh
go test ./...
go vet ./...
```

GitHub Actions also runs tests on Linux and Windows, the race detector on
Linux, and builds binaries for both platforms.
