// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
)

type routerConnectionTotals struct {
	Upload, Download uint64
	Connections, UDP int
}

func routerJSONEnd(decoder *json.Decoder) error {
	_, err := decoder.Token()
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("multiple control JSON values")
}

// Unknown fields may contain nested future metadata. Skip them without keeping
// their contents, with a nesting bound in addition to the HTTP byte/time limits.
func routerSkipJSON(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("control JSON nesting limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); ok {
		if delimiter != '{' && delimiter != '[' {
			return errors.New("unexpected control JSON delimiter")
		}
		for decoder.More() {
			if delimiter == '{' {
				if _, err = decoder.Token(); err != nil {
					return err
				}
			}
			if err = routerSkipJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
	}
	return err
}

func routerReadConnectionTotals(reader io.Reader) (routerConnectionTotals, error) {
	var result routerConnectionTotals
	decoder := json.NewDecoder(reader)
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return result, errors.New("expected connection snapshot object")
	}
	seen := make(map[string]bool, 3)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return result, err
		}
		key, ok := token.(string)
		if !ok {
			return result, errors.New("invalid snapshot key")
		}
		switch key {
		case "uploadTotal", "downloadTotal", "connections":
			if seen[key] {
				return result, errors.New("duplicate snapshot field")
			}
			seen[key] = true
		}
		switch key {
		case "uploadTotal":
			err = decoder.Decode(&result.Upload)
		case "downloadTotal":
			err = decoder.Decode(&result.Download)
		case "connections":
			token, err = decoder.Token()
			if err != nil {
				return result, err
			}
			if token == nil { // Both cores can return null when no connections exist.
				continue
			}
			if token != json.Delim('[') {
				return result, errors.New("invalid connection list")
			}
			for decoder.More() {
				var connection struct {
					Metadata struct{ Network string } `json:"metadata"`
				}
				if err = decoder.Decode(&connection); err != nil {
					return result, err
				}
				result.Connections++
				if connection.Metadata.Network == "udp" {
					result.UDP++
				}
			}
			_, err = decoder.Token()
		default:
			err = routerSkipJSON(decoder, 0)
		}
		if err != nil {
			return result, err
		}
	}
	if _, err = decoder.Token(); err != nil {
		return result, err
	}
	if len(seen) != 3 {
		return result, errors.New("incomplete connection snapshot")
	}
	return result, routerJSONEnd(decoder)
}

func ReadRouterReadiness(details bool) RouterReadiness {
	m := currentRouterCoreManager()
	if m != nil {
		m.mu.RLock()
		name, phase, conf, active := m.state.Current, m.phase, m.conf, m.active
		m.mu.RUnlock()
		if phase != "ready" {
			return RouterReadiness{Core: name, Phase: "core_" + phase}
		}
		if name != "dae" {
			result := RouterReadiness{Core: name, Phase: "starting", Configured: conf != EmptyConfig}
			if s, ok := active.(*routerSingBoxSession); ok {
				s = s.current()
				result.Listening = s.ready.Load()
				result.Ready = result.Listening
				result.Initialized = result.Listening
				result.DNSReady = result.Listening
				result.RoutingReady = result.Listening
				result.RestartDNS = "core_specific_cache"
				if result.Ready {
					result.Phase = "ready"
					if !result.Configured {
						result.Phase = "inactive"
					}
				}
			}
			return result
		}
	}
	result := readRouterDaeReadiness(details)
	result.Core = "dae"
	return result
}
func routerCoreOverview(windowSec, maxPoints int) (*RuntimeOverview, bool, error) {
	m := currentRouterCoreManager()
	if m == nil {
		return nil, false, nil
	}
	m.mu.RLock()
	name, active := m.state.Current, m.active
	m.mu.RUnlock()
	if name == "dae" {
		return nil, false, nil
	}
	original, ok := active.(*routerSingBoxSession)
	if !ok {
		return nil, true, errors.New("core_not_ready")
	}
	s := original.current()
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	now := time.Now()
	if !now.Before(s.statsNextAt) {
		var connections routerConnectionTotals
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := s.readAPI(ctx, "/connections", 16<<20, func(reader io.Reader) error {
			var err error
			connections, err = routerReadConnectionTotals(reader)
			return err
		})
		cancel()
		now = time.Now()
		s.statsNextAt = now.Add(time.Second)
		s.statsError = err
		if err != nil {
			if s.log != nil && (s.statsLogAt.IsZero() || now.Sub(s.statsLogAt) >= 30*time.Second) {
				s.statsLogAt = now
				s.log.WithField("core", name).WithField("code", err.Error()).Warn("Core runtime statistics unavailable; proxy configuration unchanged")
			}
			return nil, true, err
		}
		// Large tables are expensive for the native core to serialize too. Share
		// one reduced sample between tabs; never speed up retries after a failure.
		if connections.Connections >= 8192 {
			s.statsNextAt = now.Add(4 * time.Second)
		} else if connections.Connections >= 2048 {
			s.statsNextAt = now.Add(2 * time.Second)
		}
		next := &RuntimeOverview{UpdatedAt: now, UploadTotal: connections.Upload, DownloadTotal: connections.Download, ActiveConnections: connections.Connections, UDPSessions: connections.UDP}
		if previous := s.stats; previous != nil {
			seconds := now.Sub(previous.UpdatedAt).Seconds()
			if next.UploadTotal >= previous.UploadTotal {
				next.UploadRate = uint64(float64(next.UploadTotal-previous.UploadTotal) / seconds)
			}
			if next.DownloadTotal >= previous.DownloadTotal {
				next.DownloadRate = uint64(float64(next.DownloadTotal-previous.DownloadTotal) / seconds)
			}
		}
		s.stats = next
		s.samples = append(s.samples, RuntimeTrafficSample{Timestamp: now, UploadRate: next.UploadRate, DownloadRate: next.DownloadRate})
		first := 0
		for first < len(s.samples) && (now.Sub(s.samples[first].Timestamp) > time.Hour || len(s.samples)-first > 7200) {
			first++
		}
		if first > 0 {
			s.samples = append([]RuntimeTrafficSample(nil), s.samples[first:]...)
		}
	}
	if s.statsError != nil {
		return nil, true, s.statsError
	}
	result := *s.stats
	if windowSec < 1 {
		windowSec = 60
	}
	if windowSec > 3600 {
		windowSec = 3600
	}
	if maxPoints < 2 {
		maxPoints = 60
	}
	if maxPoints > 600 {
		maxPoints = 600
	}
	var available []RuntimeTrafficSample
	for _, sample := range s.samples {
		if now.Sub(sample.Timestamp) <= time.Duration(windowSec)*time.Second {
			available = append(available, sample)
		}
	}
	if len(available) <= maxPoints {
		result.Samples = available
	} else {
		for i := 0; i < maxPoints; i++ {
			result.Samples = append(result.Samples, available[i*(len(available)-1)/(maxPoints-1)])
		}
	}
	return &result, true, nil
}
