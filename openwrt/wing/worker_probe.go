// SPDX-License-Identifier: AGPL-3.0-only
package common

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/url"
	"strings"
	"time"
)

// NaiLinkWorkerProbeInterval limits background probes of recognizable Workers
// endpoints. It is not a limit on user traffic or on an entire Cloudflare account.
const NaiLinkWorkerProbeInterval = 5 * time.Minute

func NaiLinkWorkerHost(host string) bool {
	host = strings.TrimSpace(host)
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return len(host) > len(".workers.dev") && strings.HasSuffix(host, ".workers.dev") &&
		!strings.ContainsAny(host, " /\\?#@,:\r\n\t")
}

// NaiLinkWorkerLink checks node endpoints and handshake fields, never the
// display name, path or test target. dae separates the hops of a chain with ->.
func NaiLinkWorkerLink(link, address string) bool {
	for _, endpoint := range strings.Split(address, "->") {
		if NaiLinkWorkerHost(endpoint) {
			return true
		}
	}
	for _, hop := range strings.Split(link, "->") {
		u, err := url.Parse(strings.TrimSpace(hop))
		if err == nil && u.Scheme != "" {
			if NaiLinkWorkerHost(u.Hostname()) {
				return true
			}
			query := u.Query()
			for _, key := range []string{"host", "sni", "peer", "serverName"} {
				if NaiLinkWorkerHost(query.Get(key)) {
					return true
				}
			}
		}
		// Legacy VMess subscription links embed the WebSocket Host in JSON.
		raw, ok := strings.CutPrefix(strings.TrimSpace(hop), "vmess://")
		if !ok || len(raw) > 32768 {
			continue
		}
		raw, _, _ = strings.Cut(raw, "#")
		for _, encoding := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
			data, err := encoding.DecodeString(strings.TrimRight(raw, "="))
			if err != nil {
				continue
			}
			var fields struct{ Add, Host, SNI string }
			if json.Unmarshal(data, &fields) == nil &&
				(NaiLinkWorkerHost(fields.Add) || NaiLinkWorkerHost(fields.Host) || NaiLinkWorkerHost(fields.SNI)) {
				return true
			}
			break
		}
	}
	return false
}
