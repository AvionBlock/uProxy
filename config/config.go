package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type ProxyConfig struct {
	ListenPort int    `json:"listenPort"`
	ServerHost string `json:"serverHost"`
	ServerPort int    `json:"serverPort"`
	ProtoTCP   bool   `json:"protoTCP"`
}

type ReverseProxyConfig struct {
	ListenPort  int    `json:"listenPort"`
	CertFile    string `json:"certFile"`
	KeyFile     string `json:"keyFile"`
	ServerHost  string `json:"serverHost"`  // Может быть IP:PORT или домен:PORT
	ServerProto string `json:"serverProto"` // http или https
}

type Config struct {
	DebugMode      bool                 `json:"debugMode"`
	Proxies        []ProxyConfig        `json:"proxies"`
	ReverseProxies []ReverseProxyConfig `json:"reverseProxies"`
}

func LoadConfig(path string) (*Config, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		var cfg Config
		cfg.Proxies = []ProxyConfig{}
		cfg.ReverseProxies = []ReverseProxyConfig{}

		fmt.Print("Enable debug mode? (true/false): ")
		fmt.Scan(&cfg.DebugMode)

		for {
			fmt.Println("Select proxy type:")
			fmt.Println("1 = TCP")
			fmt.Println("2 = UDP")
			fmt.Println("3 = Reverse HTTP(S) Proxy")
			fmt.Print("Your choice: ")

			var choice int
			fmt.Scan(&choice)

			switch choice {
			case 1, 2:
				var proxy ProxyConfig

				fmt.Print("Listening port: ")
				fmt.Scan(&proxy.ListenPort)

				fmt.Print("Remote server IP or hostname: ")
				fmt.Scan(&proxy.ServerHost)

				fmt.Print("Remote server port: ")
				fmt.Scan(&proxy.ServerPort)

				proxy.ProtoTCP = choice == 1
				cfg.Proxies = append(cfg.Proxies, proxy)

			case 3:
				var rp ReverseProxyConfig

				fmt.Print("Listening port (443 recommended): ")
				fmt.Scan(&rp.ListenPort)

				fmt.Print("Full path to SSL certificate file (.pem): ")
				fmt.Scan(&rp.CertFile)

				fmt.Print("Full path to SSL private key file (.key): ")
				fmt.Scan(&rp.KeyFile)

				fmt.Print("Remote backend (e.g. 88.99.140.161:2920 or domain.com:443): ")
				fmt.Scan(&rp.ServerHost)

				for {
					fmt.Print("Backend protocol (http or https): ")
					fmt.Scan(&rp.ServerProto)
					rp.ServerProto = strings.ToLower(rp.ServerProto)
					if rp.ServerProto == "http" || rp.ServerProto == "https" {
						break
					}
					fmt.Println("Invalid protocol. Choose 'http' or 'https'")
				}

				cfg.ReverseProxies = append(cfg.ReverseProxies, rp)

			default:
				fmt.Println("Invalid choice.")
			}

			var more string
			fmt.Print("Add another proxy config? (y/n): ")
			fmt.Scan(&more)
			if strings.ToLower(more) != "y" {
				break
			}
		}

		// Save to file
		file, err := os.Create(path)
		if err != nil {
			return nil, err
		}
		defer file.Close()

		encoder := json.NewEncoder(file)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(cfg); err != nil {
			return nil, err
		}

		return &cfg, nil
	}

	// Load existing config
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var cfg Config
	if err := json.NewDecoder(file).Decode(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
