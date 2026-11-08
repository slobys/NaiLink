// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import (
	"errors"
	"regexp"
	"strings"
)

// dae writes the outer dialer first: landing -> entry.
const ChainSeparator = "->"

var chainHopScheme = regexp.MustCompile(`^[a-z][a-z0-9+.-]*://`)

func ChainLinks(link string) (entry, landing string, err error) {
	landing, entry, found := strings.Cut(link, ChainSeparator)
	entry, landing = strings.TrimSpace(entry), strings.TrimSpace(landing)
	entryEndpoint, _, _ := strings.Cut(entry, "#")
	landingEndpoint, _, _ := strings.Cut(landing, "#")
	if !found || strings.Count(link, "->") != 1 || strings.ContainsAny(link, "\r\n\x00") ||
		!chainHopScheme.MatchString(entry) || !chainHopScheme.MatchString(landing) || entryEndpoint == landingEndpoint {
		return "", "", errors.New("invalid_proxy_chain")
	}
	return entry, landing, nil
}

func chainNode(link, tag string, opt NodeOptions) (Object, error) {
	if tag == "" {
		return nil, errors.New("invalid_node_name")
	}
	entryLink, landingLink, err := ChainLinks(link)
	if err != nil {
		return nil, err
	}
	// Keep the private hop outside the public node and group tag namespaces.
	entryTag := "chain-entry:" + tag
	entry, err := Node(entryLink, entryTag, opt)
	if err != nil {
		return nil, err
	}
	landing, err := Node(landingLink, tag, opt)
	if err != nil {
		return nil, err
	}
	for _, hop := range []Object{entry, landing} {
		if hop["detour"] != nil || hop["_nailink_children"] != nil || NativeFeature(hop) != "" {
			return nil, errors.New("unsupported_chain_hop")
		}
	}
	landing["detour"] = entryTag
	landing["_nailink_children"] = []Object{entry}
	return landing, nil
}
