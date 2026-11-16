// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

var vlessUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// RequiresMihomo selects the shared metadata parser at import time. It does
// not attest to validity: Node still validates every option before saving.
func RequiresMihomo(link string) bool {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Scheme != "vless" {
		return false
	}
	q := u.Query()
	for _, key := range []string{"type", "net", "network"} {
		if q.Get(key) == "xhttp" {
			return true
		}
	}
	return q.Get("encryption") != "" && q.Get("encryption") != "none"
}

// These are the common, equivalent fields implemented by the pinned mihomo.
// Other Xray settings must not disappear during conversion.
var xhttpJSONKeys = map[string]string{
	"path": "path", "host": "host", "mode": "mode", "headers": "headers",
	"x-padding-bytes": "xPaddingBytes", "no-grpc-header": "noGRPCHeader",
}

func validateXHTTP(o Object) error {
	for key, value := range o {
		if _, ok := xhttpJSONKeys[key]; !ok {
			return OptionProblem("unsupported_transport_option", key)
		}
		switch key {
		case "no-grpc-header":
			if _, ok := value.(bool); !ok {
				return OptionProblem("unsupported_transport_option", key)
			}
		case "headers":
			headers, ok := value.(map[string]any)
			if !ok || len(headers) > 32 {
				return OptionProblem("unsupported_transport_option", key)
			}
			for name, raw := range headers {
				text, ok := raw.(string)
				if !ok || name == "" || len(name) > 128 || len(text) > 2048 || strings.ContainsAny(name, "\x00\r\n :\t") || strings.ContainsAny(text, "\x00\r\n") {
					return OptionProblem("unsupported_transport_option", "headers")
				}
			}
		default:
			text, ok := value.(string)
			if !ok || len(text) > 2048 || strings.ContainsAny(text, "\x00\r\n") {
				return OptionProblem("unsupported_transport_option", key)
			}
			switch key {
			case "mode":
				if text != "" && text != "auto" && text != "packet-up" && text != "stream-up" && text != "stream-one" {
					return OptionProblem("unsupported_transport_option", key)
				}
			case "path":
				if text != "" && !strings.HasPrefix(text, "/") {
					return OptionProblem("unsupported_transport_option", key)
				}
			case "x-padding-bytes":
				if text != "" {
					bounds := strings.Split(text, "-")
					if len(bounds) != 2 {
						return OptionProblem("unsupported_transport_option", key)
					}
					lo, e1 := strconv.Atoi(bounds[0])
					hi, e2 := strconv.Atoi(bounds[1])
					if e1 != nil || e2 != nil || lo < 0 || hi < lo || hi > 65535 {
						return OptionProblem("unsupported_transport_option", key)
					}
				}
			}
		}
	}
	return nil
}

func xhttpQuery(q url.Values) (Object, error) {
	o := Object{}
	if extra := q.Get("extra"); extra != "" {
		if len(extra) > 16384 {
			return nil, OptionProblem("unsupported_transport_option", "extra")
		}
		// Decode top-level keys explicitly so conflicting duplicate keys cannot
		// bypass the equality checks against the URI's shorthand fields.
		d := json.NewDecoder(strings.NewReader(extra))
		token, err := d.Token()
		if err != nil || token != json.Delim('{') {
			return nil, OptionProblem("unsupported_transport_option", "extra")
		}
		seen := map[string]bool{}
		for d.More() {
			token, err := d.Token()
			if err != nil {
				return nil, OptionProblem("unsupported_transport_option", "extra")
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return nil, OptionProblem("conflicting_node_options", "extra")
			}
			seen[key] = true
			canonical := ""
			for name, alias := range xhttpJSONKeys {
				if alias == key {
					canonical = name
				}
			}
			if canonical == "" {
				return nil, OptionProblem("unsupported_transport_option", key)
			}
			var value any
			if d.Decode(&value) != nil {
				return nil, OptionProblem("unsupported_transport_option", "extra")
			}
			o[canonical] = value
		}
		if _, err := d.Token(); err != nil {
			return nil, OptionProblem("unsupported_transport_option", "extra")
		}
		if d.Decode(new(any)) != io.EOF {
			return nil, OptionProblem("unsupported_transport_option", "extra")
		}
	}
	for query, key := range map[string]string{"path": "path", "host": "host", "mode": "mode", "x_padding_bytes": "x-padding-bytes"} {
		if value := q.Get(query); value != "" {
			if previous, exists := o[key]; exists && !reflect.DeepEqual(previous, value) {
				return nil, OptionProblem("conflicting_node_options", query, "extra")
			}
			o[key] = value
		}
	}
	return o, validateXHTTP(o)
}

func validateVLESSEncryption(value string) error {
	bad := func() error { return OptionProblem("unsupported_vless_encryption", "encryption") }
	parts := strings.Split(value, ".")
	if len(value) > 16384 || len(parts) < 4 || len(parts) > 11 || parts[0] != "mlkem768x25519plus" {
		return bad()
	}
	if parts[1] != "native" && parts[1] != "xorpub" && parts[1] != "random" {
		return bad()
	}
	if parts[2] != "0rtt" && parts[2] != "1rtt" {
		return bad()
	}
	for _, key := range parts[3:] {
		raw, err := base64.RawURLEncoding.DecodeString(key)
		if err != nil || (len(raw) != 32 && len(raw) != 1184) {
			return bad()
		}
	}
	return nil
}
