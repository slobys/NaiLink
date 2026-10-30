// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/daeuniverse/dae-wing/routercore"
	daeConfig "github.com/daeuniverse/dae/config"
)

func routerParseCoreNode(engine, link, tag string, opt routercore.NodeOptions) (sbObject, error) {
	if engine == "mihomo" && strings.HasPrefix(strings.TrimSpace(link), "ssr://") {
		return routercore.MihomoSSR(strings.TrimSpace(link), tag)
	}
	return routercore.Node(link, tag, opt)
}
func routerMihomoInstalled() bool {
	st, e := os.Lstat(routerMihomoBinary)
	return e == nil && st.Mode().IsRegular() && st.Mode().Perm()&0022 == 0 && st.Mode().Perm()&0111 != 0 && st.Size() > 0 && st.Size() < 200<<20
}
func routerMihomoVersion(ctx context.Context) error {
	if !routerMihomoInstalled() {
		return errors.New("mihomo_not_installed")
	}
	command := exec.CommandContext(ctx, routerMihomoBinary, "-v")
	var output routerCoreBoundedWriter
	command.Stdout = &output
	command.Stderr = io.Discard
	if command.Run() != nil || !strings.HasPrefix(string(output.data), "Mihomo Meta v"+routercore.MihomoVersion+" ") {
		return errors.New("mihomo_version_mismatch")
	}
	return nil
}
func routerCheckNative(ctx context.Context, conf *daeConfig.Config, dirs []string, transparent bool, engine string) error {
	if engine != "mihomo" {
		return routerCheckSingBox(ctx, conf, dirs, transparent)
	}
	ports := sbPorts{Mixed: 39001, API: 39002, DNS: 39003, TProxy: 39006, Mode: routerMode(ctx), Secret: "offline-configuration-check"}
	profile, _, e := coreCanonicalConfig(conf, dirs, ports, transparent, engine)
	if e != nil {
		return e
	}
	profile, e = mihomoFromCanonical(profile, conf, ports)
	if e != nil {
		return e
	}
	return routerCheckNativeProfile(ctx, profile, engine)
}
func routerCheckNativeProfile(ctx context.Context, profile sbObject, engine string) error {
	if engine != "mihomo" {
		return routerCheckSingBoxProfile(ctx, profile)
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if e := routerMihomoVersion(ctx); e != nil {
		return e
	}
	if e := routercore.PrivateDir(routerCoreRuntimeDir); e != nil {
		return e
	}
	dir, e := os.MkdirTemp(routerCoreRuntimeDir, "check-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(dir)
	clean := sbObject{}
	for k, v := range profile {
		if !strings.HasPrefix(k, "_nailink_") {
			clean[k] = v
		}
	}
	raw, e := json.Marshal(clean)
	if e != nil {
		return e
	}
	path := filepath.Join(dir, "config.json")
	if e = routercore.Write(path, raw); e != nil {
		return e
	}
	command := exec.CommandContext(ctx, routerMihomoBinary, "-t", "-d", dir, "-f", path)
	if e = command.Run(); e != nil {
		if ctx.Err() != nil {
			return errors.New("core_configuration_check_timeout")
		}
		return errors.New("mihomo_configuration_invalid")
	}
	return nil
}
