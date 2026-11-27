package config

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type prompter struct {
	in  *bufio.Scanner
	out io.Writer
}

func (p *prompter) ask(label, def string) string {
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", label)
	}
	if !p.in.Scan() {
		return def
	}
	s := strings.TrimSpace(p.in.Text())
	if s == "" {
		return def
	}
	return s
}

func (p *prompter) askInt(label string, def int) int {
	for {
		s := p.ask(label, strconv.Itoa(def))
		v, err := strconv.Atoi(s)
		if err == nil && v > 0 && v <= 65535 {
			return v
		}
		fmt.Fprintln(p.out, "Please enter a port between 1 and 65535.")
	}
}

func (p *prompter) askBool(label string, def bool) bool {
	d := "n"
	if def {
		d = "y"
	}
	for {
		s := strings.ToLower(p.ask(label+" (y/n)", d))
		switch s {
		case "y", "yes", "true":
			return true
		case "n", "no", "false":
			return false
		}
		fmt.Fprintln(p.out, "Please answer y or n.")
	}
}

func (p *prompter) askChoice(label string, choices []string, def string) string {
	for {
		s := strings.ToLower(p.ask(label, def))
		for _, c := range choices {
			if s == c {
				return c
			}
		}
		fmt.Fprintf(p.out, "Please choose one of: %s\n", strings.Join(choices, ", "))
	}
}

// runWizard builds a config interactively. It is used when config.json is missing.
func runWizard(in io.Reader, out io.Writer) (*Config, error) {
	p := &prompter{in: bufio.NewScanner(in), out: out}
	cfg := &Config{Proxies: []ProxyConfig{}, ReverseProxies: []ReverseProxyConfig{}}

	fmt.Fprintln(out, "No config.json found, let's create one.")
	cfg.DebugMode = p.askBool("Enable debug mode?", false)

	for {
		fmt.Fprintln(out, "Select proxy type:")
		fmt.Fprintln(out, "  1 = TCP        (Minecraft Java, generic TCP)")
		fmt.Fprintln(out, "  2 = UDP        (RakNet / NetherNet media only)")
		fmt.Fprintln(out, "  3 = Bedrock    (TCP + UDP on one port: Geyser / BDS with NetherNet, recommended)")
		fmt.Fprintln(out, "  4 = Reverse HTTP(S) proxy")
		choice := p.askChoice("Your choice", []string{"1", "2", "3", "4"}, "3")

		switch choice {
		case "1", "2", "3":
			var proxy ProxyConfig
			switch choice {
			case "1":
				proxy.Protocol = ProtocolTCP
			case "2":
				proxy.Protocol = ProtocolUDP
			default:
				proxy.Protocol = ProtocolBedrock
			}
			proxy.Name = p.ask("Name (for logs)", string(proxy.Protocol))
			proxy.ListenIP = p.ask("Listening IP", "0.0.0.0")
			defPort := 25565
			if proxy.Protocol != ProtocolTCP {
				defPort = 19132
			}
			proxy.ListenPort = p.askInt("Listening port", defPort)
			proxy.ServerHost = p.ask("Remote server IP or hostname", "127.0.0.1")
			proxy.ServerPort = p.askInt("Remote server port", defPort)
			pp := p.askBool("Send PROXY protocol v2 headers to the backend (Geyser/Velocity/Paper with haproxy enabled)?", true)
			proxy.ProxyProtocol = &pp
			if proxy.Protocol != ProtocolTCP && pp {
				fmt.Fprintln(out, "UDP header mode:")
				fmt.Fprintln(out, "  auto      = detect RakNet / NetherNet per flow (recommended)")
				fmt.Fprintln(out, "  raknet    = header on the first datagram of a flow (classic Geyser)")
				fmt.Fprintln(out, "  nethernet = header on every datagram (Geyser webrtc-proxy-protocol-relays)")
				fmt.Fprintln(out, "  plain     = never add a header (Bedrock Dedicated Server)")
				proxy.UDPMode = UDPMode(p.askChoice("Mode", []string{"auto", "raknet", "nethernet", "plain"}, "auto"))
			}
			if err := proxy.Validate(); err != nil {
				fmt.Fprintf(out, "Invalid entry: %v\n", err)
				continue
			}
			cfg.Proxies = append(cfg.Proxies, proxy)

		case "4":
			var rp ReverseProxyConfig
			rp.ListenPort = p.askInt("Listening port", 443)
			rp.CertFile = p.ask("Full path to TLS certificate file (.pem/.crt)", "")
			rp.KeyFile = p.ask("Full path to TLS private key file (.key)", "")
			rp.Domain = p.ask("Domain to serve (empty = take it from the certificate)", "")
			rp.ServerHost = p.ask("Remote backend (e.g. 127.0.0.1:19132 or domain.com:443)", "")
			rp.ServerProto = p.askChoice("Backend protocol", []string{"http", "https"}, "http")
			if err := rp.Validate(); err != nil {
				fmt.Fprintf(out, "Invalid entry: %v\n", err)
				continue
			}
			cfg.ReverseProxies = append(cfg.ReverseProxies, rp)
		}

		if !p.askBool("Add another proxy config?", false) {
			break
		}
	}

	return cfg, nil
}
