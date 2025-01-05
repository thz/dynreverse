# "dynreverse" UDP reverse proxy

A UDP reverse proxy forwarding packets to an upstream server identified by DNS name, which is subject to change over time.

## Problem

In cases where some component resolves a DNS name only once, and then uses the resolved IP address for the lifetime of the component, a change in the IP address of the upstream server will break the connection. Wireguard is one such example. Especially when the endpoint is using a dynamic IP address, re-resolving the endpoint's DNS is desperately needed. For wireguard specifically, usually hooks are recommended to re-resolve the DNS name and update the peer. Unfortunately that is not possible for the integrated OSX wireguard client.

## Solution

I created this reverse proxy to run on my local machine offering an endpoint for the wireguard client (e.g. `127.0.0.1:50001`). The reverse proxy resolves the DNS name (and continues doing so) and forwards packets to the resolved IP address (upstream / actual wireguard peer).

`dynreverse` will periodically resolve the DNS name and update the upstream connection.

## Usage

```bash
$ dynreverse reverse --upstream-endpoint peer.example.com:51820 --listen-address 127.0.0.1:50001
```

## Installation

Either build:

```bash
$ go build -o dynreverse ./cmd/dynreverse
```

Or download the latest binary release:

```bash
$ curl -JLO https://github.com/thz/dynreverse/releases/latest/download/dynreverse-darwin-arm64
$ curl -JLO https://github.com/thz/dynreverse/releases/latest/download/dynreverse-darwin-amd64
$ curl -JLO https://github.com/thz/dynreverse/releases/latest/download/dynreverse-linux-amd64
$ curl -JLO https://github.com/thz/dynreverse/releases/latest/download/dynreverse-linux-arm64
```

On OSX you might want to make this a "launch agent":

```bash
$ mkdir -p ~/Library/com.github.thz.dynreverse
$ cp dynreverse ~/Library/com.github.thz.dynreverse/

cat > ~/Library/LaunchAgents/com.github.thz.dynreverse.plist <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key>
    <string>com.github.thz.dynreverse</string>
    <key>Program</key>
    <string>/Users/thz/Library/com.github.thz.dynreverse/dynreverse</string>
    <key>ProgramArguments</key>
    <array>
      <string>dynreverse</string>
      <string>reverse</string>
      <string>--upstream-endpoint</string>
      <string>peer.example.com:51820</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
  </dict>
</plist>
EOF

$ launchctl load ~/Library/LaunchAgents/com.github.thz.dynreverse.plist
```
