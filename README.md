# uproxy

A lightweight and configurable **TCP/UDP/HTTPS reverse proxy** with support for Proxy Protocol v2, written in Go.

## Features

- HTTPS reverse proxy with custom TLS config and header forwarding
- TCP proxy with Proxy Protocol v2 header support
- UDP proxy supporting multiple clients, Proxy Protocol v2, and per-client sockets (mcbe)
- Connection logging and debug mode
- Graceful error handling and client timeout management

## Requirements

- Go xD

## Installation

```bash
git clone https://github.com/yourusername/uproxy.git
cd uproxy
go build -o uproxy ./cmd/uproxy
```

Or just download from releases page
