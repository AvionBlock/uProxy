package proxies

import (
	"context"
	"errors"

	"uproxy/config"
)

// StartBedrockProxy serves one Bedrock endpoint: TCP and UDP on the same
// listen port, both forwarded to the same backend port.
//
// Since Bedrock 26.50 the client first asks the server over HTTP (TCP) how to
// connect (NetherNet signaling) and then plays over WebRTC (UDP). Older clients,
// and servers running Geyser with "transport: both", still use RakNet over UDP
// on the same port. One "bedrock" entry covers all of that; the UDP side picks
// the right PROXY protocol convention per flow.
func StartBedrockProxy(ctx context.Context, cfg config.ProxyConfig) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 2)
	go func() { errCh <- NewTCPProxy(cfg).Run(ctx) }()
	go func() { errCh <- NewUDPProxy(cfg).Run(ctx) }()

	var errs []error
	for i := 0; i < 2; i++ {
		err := <-errCh
		if err != nil {
			errs = append(errs, err)
			// One half failed to start: stop the other one too, a half-working
			// Bedrock endpoint only confuses players.
			cancel()
		}
	}
	return errors.Join(errs...)
}

// StartProxy dispatches an L4 entry to the right implementation.
func StartProxy(ctx context.Context, cfg config.ProxyConfig) error {
	switch cfg.EffectiveProtocol() {
	case config.ProtocolTCP:
		return StartTCPProxy(ctx, cfg)
	case config.ProtocolUDP:
		return StartUDPProxy(ctx, cfg)
	case config.ProtocolBedrock:
		return StartBedrockProxy(ctx, cfg)
	default:
		return errors.New("unknown protocol " + string(cfg.EffectiveProtocol()))
	}
}
