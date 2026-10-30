// SPDX-License-Identifier: AGPL-3.0-only
package dae

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/daeuniverse/dae-wing/routercore"
	"github.com/vishvananda/netlink"
)

var routerCoreFW4Include = "/etc/nftables.d/0-nailink-core.nft"

func routerCoreAllRoutes() ([]netlink.Route, error) {
	// RT_TABLE_UNSPEC plus an explicit table filter requests every table.
	return netlink.RouteListFiltered(netlink.FAMILY_ALL, &netlink.Route{Table: 0}, netlink.RT_FILTER_TABLE)
}

type routerCoreNetworkOwner struct {
	Bridges        []routerTPBridge `json:"bridges,omitempty"`
	Mode           string           `json:"mode,omitempty"`
	ProxyPort      int              `json:"proxy_port,omitempty"`
	DNSPort        int              `json:"dns_port,omitempty"`
	LAN            []string         `json:"lan,omitempty"`
	IPv6           bool             `json:"ipv6,omitempty"`
	Active         bool             `json:"active,omitempty"`
	Core           string           `json:"core,omitempty"`
	Config         string           `json:"config"`
	PID            int              `json:"pid"`
	Started        string           `json:"started"`
	BaselineTables []int            `json:"baseline_tables"`
	RedirectTable  int              `json:"redirect_table,omitempty"`
	BootID         string           `json:"boot_id"`
}

func routerBootID() string {
	data, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(data))
}
func routerClearCoreFW4(ctx context.Context) error {
	if info, err := os.Lstat(routerCoreFW4Include); os.IsNotExist(err) {
		return nil
	} else if err != nil || !info.Mode().IsRegular() {
		return errors.New("core_network_ownership_failed")
	}
	data, err := os.ReadFile(routerCoreFW4Include)
	if err != nil || (!bytes.HasPrefix(data, []byte("# NaiLink managed TUN access; no chain policy changes\n")) && !bytes.HasPrefix(data, []byte("# NaiLink managed TPROXY access\n"))) || !bytes.Contains(data, []byte("!nailink-core")) {
		return errors.New("core_network_ownership_failed")
	}
	if err = os.Remove(routerCoreFW4Include); err != nil {
		return errors.New("core_cleanup_failed")
	}
	if err = exec.CommandContext(ctx, "fw4", "reload").Run(); err != nil {
		return errors.New("core_cleanup_failed")
	}
	return nil
}

// sing-tun chooses a random redirect table. A private pre-start snapshot and
// its exact loopback-only route shape identify leftovers; never guess a table
// number observed on another machine or delete unrelated priority-1 rules.
func routerRedirectTable(owner routerCoreNetworkOwner, routes []netlink.Route) (int, error) {
	if owner.RedirectTable != 0 {
		if owner.RedirectTable <= 255 || owner.RedirectTable == 20340 {
			return 0, errors.New("core_network_ownership_failed")
		}
		for _, route := range routes {
			if route.Table == owner.RedirectTable && (route.Type != syscall.RTN_LOCAL || route.Dst == nil || route.Dst.String() != "127.0.0.1/32" && route.Dst.String() != "::1/128") {
				return 0, errors.New("core_network_ownership_failed")
			}
		}
		return owner.RedirectTable, nil
	}
	baseline := map[int]bool{0: true, 20340: true, 253: true, 254: true, 255: true}
	for _, table := range owner.BaselineTables {
		baseline[table] = true
	}
	candidates := map[int]bool{}
	for _, route := range routes {
		if baseline[route.Table] {
			continue
		}
		valid := route.Type == syscall.RTN_LOCAL && route.Dst != nil && (route.Dst.String() == "127.0.0.1/32" || route.Dst.String() == "::1/128")
		if previous, ok := candidates[route.Table]; !ok {
			candidates[route.Table] = valid
		} else {
			candidates[route.Table] = previous && valid
		}
	}
	selected := 0
	for table, valid := range candidates {
		if valid {
			if selected != 0 {
				return 0, errors.New("core_network_ownership_failed")
			}
			selected = table
		}
	}
	return selected, nil
}

func routerRecordRedirectTable(root string) error {
	owner, err := routerReadNetworkOwner(root)
	if err != nil {
		return err
	}
	if owner.Core == "mihomo" {
		// MetaCubeX sing-tun v0.4.28 does not create the SagerNet random
		// loopback redirect table. Require its own TUN route and nft table.
		routes, e := routerCoreAllRoutes()
		if e != nil {
			return errors.New("core_network_check_failed")
		}
		link, e := netlink.LinkByName(routercore.TunName)
		if e != nil {
			return errors.New("core_not_ready")
		}
		found := false
		for _, r := range routes {
			if r.Table == 20340 && r.LinkIndex == link.Attrs().Index {
				found = true
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		families, e := routerNftTablesFor(ctx, "mihomo")
		if e != nil {
			return e
		}
		if !found || len(families) == 0 {
			return errors.New("core_not_ready")
		}
		return nil
	}
	if owner.RedirectTable != 0 {
		return nil
	}
	routes, err := routerCoreAllRoutes()
	if err != nil {
		return err
	}
	table, err := routerRedirectTable(owner, routes)
	if err != nil {
		return err
	}
	if table == 0 {
		return errors.New("core_not_ready")
	}
	owner.RedirectTable = table
	data, _ := json.Marshal(owner)
	return routercore.Write(routerOwnerPath(root), data)
}

func routerOwnerPath(root string) string { return filepath.Join(routercore.Root(root), "network.json") }
func routerProcessStart(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	i := bytes.LastIndex(data, []byte(") "))
	if i < 0 {
		return ""
	}
	fields := strings.Fields(string(data[i+2:]))
	if len(fields) < 20 {
		return ""
	}
	return fields[19]
}
func routerNftTables(ctx context.Context) ([]string, error) {
	return routerNftTablesFor(ctx, "sing-box")
}
func routerNftTablesFor(ctx context.Context, table string) ([]string, error) {
	command := exec.CommandContext(ctx, "nft", "-j", "list", "tables")
	var output routerCoreBoundedWriter
	command.Stdout = &output
	command.Stderr = nil
	if err := command.Run(); err != nil {
		return nil, errors.New("nftables_unavailable")
	}
	var reply struct {
		Nftables []struct {
			Table *struct{ Name, Family string }
		}
	}
	if json.Unmarshal(output.data, &reply) != nil {
		return nil, errors.New("nftables_unavailable")
	}
	var families []string
	for _, entry := range reply.Nftables {
		if entry.Table != nil && entry.Table.Name == table {
			switch entry.Table.Family {
			case "inet", "ip", "ip6", "bridge":
				families = append(families, entry.Table.Family)
			default:
				return nil, errors.New("core_network_conflict")
			}
		}
	}
	return families, nil
}
func routerCheckNetworkAvailable(ctx context.Context) error {
	if _, err := net.InterfaceByName(routercore.TunName); err == nil {
		return errors.New("core_network_conflict")
	}
	if _, err := os.Lstat(routerCoreFW4Include); err == nil || !os.IsNotExist(err) {
		return errors.New("core_network_conflict")
	}
	if _, err := os.Lstat("/etc/nftables.d/0-sing-box-auto-redirect.nft"); err == nil || !os.IsNotExist(err) {
		return errors.New("core_network_conflict")
	}
	for _, table := range []string{"sing-box", "mihomo", routerTPNft, routerTPBridgeNft} {
		families, err := routerNftTablesFor(ctx, table)
		if err != nil {
			return err
		}
		if len(families) > 0 {
			return errors.New("core_network_conflict")
		}
	}
	if _, err := os.Lstat("/etc/nftables.d/0-mihomo-auto-redirect.nft"); err == nil || !os.IsNotExist(err) {
		return errors.New("core_network_conflict")
	}
	rules, err := netlink.RuleList(netlink.FAMILY_ALL)
	if err != nil {
		return errors.New("core_network_check_failed")
	}
	for _, rule := range rules {
		if rule.Priority == 1 || rule.Priority == 32768 || rule.Priority >= 20340 && rule.Priority <= 20360 || rule.Table == 20340 || rule.Table == routerTPTable || rule.Mark == routerTPMark {
			return errors.New("core_network_conflict")
		}
	}
	routes, err := routerCoreAllRoutes()
	if err != nil {
		return errors.New("core_network_check_failed")
	}
	for _, route := range routes {
		if route.Table == 20340 || route.Table == routerTPTable {
			return errors.New("core_network_conflict")
		}
	}
	return nil
}
func routerClaimSingboxNetwork(root, config string) error {
	return routerClaimNativeNetwork(root, config, "sing-box")
}
func routerClaimNativeNetwork(root, config, engine string, options ...routerCoreNetworkOwner) (result error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := os.Lstat(routerOwnerPath(root)); err == nil || !os.IsNotExist(err) {
		return errors.New("core_network_conflict")
	}
	if err := routerCheckNetworkAvailable(ctx); err != nil {
		return err
	}
	owner := routerCoreNetworkOwner{Core: engine, Config: config, BootID: routerBootID()}
	if len(options) > 0 {
		o := options[0]
		owner.Mode, owner.ProxyPort, owner.DNSPort, owner.LAN, owner.IPv6 = o.Mode, o.ProxyPort, o.DNSPort, o.LAN, o.IPv6
	}
	if owner.Mode == "tproxy" {
		var bridgeErr error
		owner.Bridges, bridgeErr = routerTPBridgeTopology(owner.LAN)
		if bridgeErr != nil {
			return bridgeErr
		}
		if err := routerTPValidateOwner(owner); err != nil {
			return err
		}
		for _, name := range owner.LAN {
			if _, err := net.InterfaceByName(name); err != nil {
				return errors.New("missing_lan_interface")
			}
		}
	}
	if owner.BootID == "" {
		return errors.New("core_environment_unsupported")
	}
	routes, routeErr := routerCoreAllRoutes()
	if routeErr != nil {
		return errors.New("core_network_check_failed")
	}
	tables := map[int]bool{}
	for _, route := range routes {
		tables[route.Table] = true
	}
	if len(tables) > 1024 {
		return errors.New("core_network_check_failed")
	}
	for table := range tables {
		owner.BaselineTables = append(owner.BaselineTables, table)
	}
	data, _ := json.Marshal(owner)
	if err := routercore.Write(routerOwnerPath(root), data); err != nil {
		return errors.New("core_state_failed")
	}
	defer func() {
		if result != nil {
			if err := routerReleaseSingboxNetwork(root); err != nil {
				result = errors.New("core_cleanup_failed")
			}
		}
	}()
	if owner.Mode == "tproxy" {
		rules, err := routerTPRules(owner, false)
		if err != nil {
			return err
		}
		if err = routerTPNftRun(ctx, rules, false); err != nil {
			return err
		}
	}
	// Own a small fw4 include instead of allowing the child to rewrite shared
	// chain policies. Rules only accept traffic to/from this managed interface.
	if _, err := exec.LookPath("fw4"); err == nil && exec.CommandContext(ctx, "nft", "list", "table", "inet", "fw4").Run() == nil {
		info, err := os.Lstat(filepath.Dir(routerCoreFW4Include))
		if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return errors.New("core_network_ownership_failed")
		}
		file, err := os.OpenFile(routerCoreFW4Include, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0644)
		if err != nil {
			return errors.New("core_network_conflict")
		}
		rules := []byte("# NaiLink managed TUN access; no chain policy changes\nchain input { iifname \"nailink0\" accept comment \"!nailink-core\"; oifname \"nailink0\" accept comment \"!nailink-core\"; }\nchain forward { iifname \"nailink0\" accept comment \"!nailink-core\"; oifname \"nailink0\" accept comment \"!nailink-core\"; }\n")
		if owner.Mode == "tproxy" {
			var names []string
			for _, name := range owner.LAN {
				names = append(names, fmt.Sprintf("%q", name))
			}
			rules = []byte(fmt.Sprintf("# NaiLink managed TPROXY access\nchain input { meta mark 0x%x accept comment \"!nailink-core\"; iifname { %s } meta l4proto { tcp, udp } th dport %d accept comment \"!nailink-core\"; }\n", routerTPMark, strings.Join(names, ", "), owner.DNSPort))
		}
		_, writeErr := file.Write(rules)
		syncErr := file.Sync()
		closeErr := file.Close()
		if writeErr != nil || syncErr != nil || closeErr != nil {
			return errors.New("core_state_failed")
		}
		if err = exec.CommandContext(ctx, "fw4", "reload").Run(); err != nil {
			_ = routerReleaseSingboxNetwork(root)
			return errors.New("core_network_check_failed")
		}
	}
	return nil
}
func routerRecordSingboxProcess(root string, pid int, config string) error {
	owner, err := routerReadNetworkOwner(root)
	if err != nil {
		return err
	}
	owner.Config, owner.PID, owner.Started = config, pid, routerProcessStart(pid)
	if owner.Started == "" {
		return errors.New("core_process_exited")
	}
	data, _ := json.Marshal(owner)
	return routercore.Write(routerOwnerPath(root), data)
}
func routerReadNetworkOwner(root string) (routerCoreNetworkOwner, error) {
	var owner routerCoreNetworkOwner
	data, err := routercore.Read(routerOwnerPath(root), 16384)
	if err != nil {
		return owner, err
	}
	if json.Unmarshal(data, &owner) != nil || owner.PID < 0 || !routercore.ValidMode(owner.Mode) || owner.Core != "" && owner.Core != "sing-box" && owner.Core != "mihomo" {
		return owner, errors.New("unsafe_core_state")
	}
	relative, err := filepath.Rel(routerCoreRuntimeDir, owner.Config)
	if err != nil || strings.HasPrefix(relative, "..") || filepath.Base(owner.Config) != "config.json" || !strings.HasPrefix(filepath.Base(filepath.Dir(owner.Config)), "active-") {
		return owner, errors.New("unsafe_core_state")
	}
	return owner, nil
}
func routerRecoverSingboxNetwork(root string) error {
	owner, err := routerReadNetworkOwner(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if owner.BootID != "" && owner.BootID != routerBootID() {
		// Kernel state cannot survive a reboot. Never act on a recycled PID or
		// random route-table ID from the previous boot; only retire our include.
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if err = routerClearCoreFW4(ctx); err != nil {
			return err
		}
		return os.Remove(routerOwnerPath(root))
	}
	if owner.PID > 0 && routerProcessStart(owner.PID) == owner.Started {
		args, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", owner.PID))
		if err == nil && len(args) > 0 {
			values := strings.Split(string(args), "\x00")
			binary, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", owner.PID))
			configFound := false
			for i := range values {
				if i > 0 && (values[i-1] == "-c" && owner.Core != "mihomo" || values[i-1] == "-f" && owner.Core == "mihomo") && values[i] == owner.Config {
					configFound = true
				}
			}
			group, groupErr := syscall.Getpgid(owner.PID)
			if !(owner.Core == "mihomo" && binary == routerMihomoBinary || owner.Core != "mihomo" && routerManagedSingBoxProgram(binary)) || !configFound || groupErr != nil || group != owner.PID {
				return errors.New("core_process_ownership_failed")
			}
			_ = syscall.Kill(-owner.PID, syscall.SIGTERM)
			deadline := time.Now().Add(6 * time.Second)
			for routerProcessStart(owner.PID) == owner.Started && time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond)
			}
			if routerProcessStart(owner.PID) == owner.Started {
				_ = syscall.Kill(-owner.PID, syscall.SIGKILL)
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
	return routerReleaseSingboxNetwork(root)
}
func routerReleaseSingboxNetwork(root string) error {
	// Cleanup requires a private claim made only after proving these resources
	// absent. Never flush nftables, fw4, conntrack or any other proxy's rules.
	owner, err := routerReadNetworkOwner(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if owner.Mode == "tproxy" {
		return routerReleaseTProxy(root, owner)
	}
	routes, err := routerCoreAllRoutes()
	if err != nil {
		return errors.New("core_cleanup_failed")
	}
	redirectTable, err := routerRedirectTable(owner, routes)
	if err != nil {
		return err
	}
	if redirectTable == 0 {
		rules, ruleErr := netlink.RuleList(netlink.FAMILY_ALL)
		if ruleErr != nil {
			return errors.New("core_cleanup_failed")
		}
		for _, rule := range rules {
			if rule.Priority == 1 {
				return errors.New("core_network_ownership_failed")
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err = routerClearCoreFW4(ctx); err != nil {
		return err
	}
	table := "sing-box"
	if owner.Core == "mihomo" {
		table = "mihomo"
	}
	families, err := routerNftTablesFor(ctx, table)
	if err != nil {
		return err
	}
	for _, family := range families {
		if err = exec.CommandContext(ctx, "nft", "delete", "table", family, table).Run(); err != nil {
			return errors.New("core_cleanup_failed")
		}
	}
	rules, err := netlink.RuleList(netlink.FAMILY_ALL)
	if err != nil {
		return errors.New("core_cleanup_failed")
	}
	for _, rule := range rules {
		if rule.Priority >= 20340 && rule.Priority <= 20360 || rule.Priority == 32768 && rule.Table == 20340 || redirectTable != 0 && rule.Priority == 1 && rule.Table == redirectTable {
			if err = netlink.RuleDel(&rule); err != nil {
				return errors.New("core_cleanup_failed")
			}
		}
	}
	routes, err = routerCoreAllRoutes()
	if err != nil {
		return errors.New("core_cleanup_failed")
	}
	for _, route := range routes {
		if route.Table != 20340 && (redirectTable == 0 || route.Table != redirectTable) {
			continue
		}
		if err = netlink.RouteDel(&route); err != nil {
			return errors.New("core_cleanup_failed")
		}
	}
	if link, err := netlink.LinkByName(routercore.TunName); err == nil {
		if err = netlink.LinkDel(link); err != nil {
			return errors.New("core_cleanup_failed")
		}
	}
	if err = routerDNSClearConntrack(owner, false); err != nil {
		return err
	}
	if err = os.Remove(routerOwnerPath(root)); err != nil && !os.IsNotExist(err) {
		return errors.New("core_state_failed")
	}
	if path := filepath.Dir(owner.Config); strings.HasPrefix(filepath.Base(path), "active-") {
		_ = os.RemoveAll(path)
	}
	return nil
}

func routerCoreManagerLock(root string) (*os.File, error) {
	if err := routercore.PrivateDir(routercore.Root(root)); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(filepath.Join(routercore.Root(root), "manager.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "core-manager.lock")
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("core_manager_already_running")
	}
	return file, nil
}

func routerManagedSingBoxProgram(path string) bool {
	if path == routerSingBoxBinary {
		return true
	}
	directory, err := routercore.ExtraDirectory(routerExtraRoot)
	return err == nil && path == filepath.Join(directory, "sing-box")
}

// Read the real WAN from the main table, never RouteGet through the active
// managed TUN. A private test core has no interface monitor of its own.
func routerMainWANInterface() (string, error) {
	for _, family := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		routes, e := netlink.RouteListFiltered(family, &netlink.Route{Table: 254}, netlink.RT_FILTER_TABLE)
		if e != nil {
			return "", errors.New("core_network_check_failed")
		}
		best, index := int(^uint(0)>>1), 0
		for _, r := range routes {
			if r.Dst != nil {
				if ones, _ := r.Dst.Mask.Size(); ones != 0 {
					continue
				}
			}
			if r.LinkIndex <= 0 || r.Type != syscall.RTN_UNICAST {
				continue
			}
			link, e := netlink.LinkByIndex(r.LinkIndex)
			if e != nil || link.Attrs().Name == routercore.TunName || link.Attrs().Flags&net.FlagLoopback != 0 {
				continue
			}
			if r.Priority < best {
				best, index = r.Priority, r.LinkIndex
			} else if r.Priority == best && index != r.LinkIndex {
				return "", errors.New("unsupported_multi_wan")
			}
		}
		if index > 0 {
			link, e := netlink.LinkByIndex(index)
			if e == nil {
				return link.Attrs().Name, nil
			}
		}
	}
	return "", errors.New("core_network_check_failed")
}
