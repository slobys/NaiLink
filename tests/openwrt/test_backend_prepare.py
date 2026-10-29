"""Verify reviewed backend patches without editing the pinned submodules."""
import importlib.util
import hashlib
import json
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("prepare_wing", ROOT / "openwrt/prepare-wing.py")
PREPARE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PREPARE)
SOURCES = (
    "go.mod",
    "webrender/webrender.go",
    "graphql/service/config/mutation_utils.go",
    "graphql/service/dns/mutation_utils.go",
    "graphql/service/routing/mutation_utils.go",
    "graphql/service/group/mutation_utils.go",
    "graphql/service/node/mutation_utils.go",
    "cmd/run.go",
    "dae/run.go",
    "dae/httpclient.go",
    "graphql/service/subscription/mutation_utils.go",
    "graphql/service/node/latency_mutation_utils.go",
    "graphql/service/node/latency_cache.go",
    "graphql/service/node/latency_resolver.go",
    "graphql/service/node/schema.go",
    "graphql/mutation.go",
    "graphql/root_schema.go",
    "dae/runtime.go",
    "db/node_utils.go",
    "db/subscription.go",
    "dae/flat_desc.go",
    "graphql/service/config/global/schema.go",
    "dae-core/config/config.go",
    "dae-core/component/outbound/dialer/dialer.go",
    "dae-core/component/outbound/dialer/register.go",
    "dae-core/component/interface_manager.go",
    "dae-core/control/dial.go", "dae-core/control/dns_control.go",
    "dae-core/control/dns_control_cache_cleanup_test.go",
    "dae-core/control/dns.go", "dae-core/control/dns_lifecycle_test.go",
    "dae-core/control/control_plane.go",
    "dae-core/control/dns_udp_packetconn_test.go",
    "dae-core/common/subscription/subscription.go",
)


@unittest.skipUnless(all((ROOT / "wing" / name).is_file() for name in SOURCES), "pinned wing checkout required")
class BackendPrepareTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        for relative in SOURCES:
            destination = self.root / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_text((ROOT / "wing" / relative).read_text(encoding="utf-8"), encoding="utf-8", newline="\n")
        (self.root / "dae-core/control").mkdir(parents=True, exist_ok=True)
        (self.root / "db").mkdir(parents=True, exist_ok=True)

    def test_runtime_patch_is_reproducible_and_idempotent(self):
        PREPARE.prepare(self.root)
        self.assertIn('routerFileServer(webFS.', (self.root / "webrender/webrender.go").read_text(encoding="utf-8"))
        cache = (self.root / "graphql/service/node/latency_cache.go").read_text(encoding="utf-8")
        self.assertIn('mergeRuntimeLatencyResults(merged, runtimeResults)', cache)
        self.assertNotIn('resolver.TestedAtV.After', cache)
        self.assertIn('source: String!', (self.root / "graphql/service/node/schema.go").read_text(encoding="utf-8"))
        self.assertEqual((self.root / "webrender/router_assets.go").read_bytes(), (ROOT / "openwrt/wing/web_assets.go").read_bytes())
        self.assertIn("return routerCheckConn(conn), nil", (self.root / "dae-core/component/outbound/dialer/dialer.go").read_text(encoding="utf-8"))
        self.assertEqual((self.root / "dae/runtime.go").read_bytes(), (ROOT / "openwrt/wing/runtime.go").read_bytes())
        self.assertIn("return c.routerRouteDial(ctx, p)", (self.root / "dae-core/control/dial.go").read_text(encoding="utf-8"))
        self.assertEqual((self.root / "dae-core/control/dial.go").read_text(encoding="utf-8").count("c.routerChooseDialTarget(ctx, outboundIndex, dst, domain)"), 2)
        for target, original in (("dae/router_core_memory.go", "core_memory.go"), ("dae-core/control/router_domain_first.go", "domain_first.go")):
            self.assertEqual((self.root / target).read_bytes(), (ROOT / "openwrt/wing" / original).read_bytes())
        self.assertIn("routerDNSPrimaryTimeout(ctx, upstream, primaryDialArg)", (self.root / "dae-core/control/dns_control.go").read_text(encoding="utf-8"))
        expiry = (self.root / "dae-core/control/dns_control_cache_cleanup_test.go").read_text(encoding="utf-8")
        self.assertIn("ctrl.evictExpiredDnsCache(deadline.Add(time.Nanosecond))", expiry)
        self.assertIn('"knowledge should exist before expiry"', expiry)
        self.assertIn('"expired cache entry should be evicted"', expiry)
        self.assertIn("c.routerResolveBootstrap(ctx, host, network, race, resolve)", (self.root / "dae-core/control/control_plane.go").read_text(encoding="utf-8"))
        self.assertIn("return pc.routerRoundTrip(ctx, data)", (self.root / "dae-core/control/dns.go").read_text(encoding="utf-8"))
        lifecycle = (self.root / "dae/run.go").read_text(encoding="utf-8")
        self.assertIn("routerSaveRestartDNS(log, c, conf)", lifecycle)
        self.assertIn("dnsCache = routerRestoreRestartDNS(log, conf, dnsCache)", lifecycle)
        self.assertEqual(lifecycle.count("routerMarkServing(c)"), 2)
        self.assertEqual(lifecycle.count("routerPublishControlPlane(c, conf)"), 2)
        self.assertIn("c.routerRestoreRouting(v, now)", (self.root / "dae-core/control/dns_control.go").read_text(encoding="utf-8"))
        self.assertEqual((self.root / "cmd/run.go").read_text(encoding="utf-8").count('mux.HandleFunc("/health/ready"'), 1)
        self.assertIn('[]string{cfgDir, cfgDir + "/geodata/current"}', (self.root / "cmd/run.go").read_text(encoding="utf-8"))
        before = {str(p.relative_to(self.root)): p.read_bytes() for p in self.root.rglob("*") if p.is_file()}
        PREPARE.prepare(self.root)
        after = {str(p.relative_to(self.root)): p.read_bytes() for p in self.root.rglob("*") if p.is_file()}
        self.assertEqual(before, after)
        for name in ("dae/router_runtime_test.go", "dae-core/control/router_runtime_fixture.go"):
            self.assertTrue((self.root / name).read_text(encoding="utf-8").startswith("//go:build daed_runtime_test\n"))

    def test_unrecognized_runtime_source_is_preserved(self):
        path = self.root / "dae/runtime.go"
        path.write_bytes(path.read_bytes() + b"// local edit\n")
        before = path.read_bytes()
        with self.assertRaisesRegex(ValueError, "Unknown backend source"):
            PREPARE.prepare(self.root)
        self.assertEqual(path.read_bytes(), before)

    def test_unknown_fixture_edit_is_preserved(self):
        PREPARE.prepare(self.root)
        path = self.root / "dae-core/control/router_runtime_fixture.go"
        path.write_bytes(path.read_bytes() + b"// local edit\n")
        before = path.read_bytes()
        with self.assertRaisesRegex(ValueError, "Unknown runtime test fixture"):
            PREPARE.prepare(self.root)
        self.assertEqual(path.read_bytes(), before)

    def test_untracked_addition_is_preserved_before_first_prepare(self):
        path = self.root / "db/router_transaction.go"
        path.write_bytes(b"// independent local addition\n")
        before = {str(p.relative_to(self.root)): p.read_bytes() for p in self.root.rglob("*") if p.is_file()}
        with self.assertRaisesRegex(ValueError, "Unknown existing backend addition"):
            PREPARE.prepare(self.root)
        after = {str(p.relative_to(self.root)): p.read_bytes() for p in self.root.rglob("*") if p.is_file()}
        self.assertEqual(before, after)

    def test_late_unknown_edit_does_not_partially_upgrade_sources(self):
        PREPARE.prepare(self.root)
        earlier = self.root / "dae/runtime.go"
        earlier.write_bytes(earlier.read_bytes() + b"// previous reviewed runtime\n")
        manifest = self.root / ".daed-openwrt-prepare.json"
        state = json.loads(manifest.read_text())
        state["dae/runtime.go"] = hashlib.sha256(earlier.read_bytes()).hexdigest()
        manifest.write_text(json.dumps(state))
        later = self.root / "dae-core/control/router_runtime_fixture.go"
        later.write_bytes(later.read_bytes() + b"// unknown late change\n")
        before = {str(p.relative_to(self.root)): p.read_bytes() for p in self.root.rglob("*") if p.is_file()}
        with self.assertRaisesRegex(ValueError, "Unknown runtime test fixture"):
            PREPARE.prepare(self.root)
        after = {str(p.relative_to(self.root)): p.read_bytes() for p in self.root.rglob("*") if p.is_file()}
        self.assertEqual(before, after)

    def test_unknown_lifecycle_edits_are_preserved(self):
        PREPARE.prepare(self.root)
        for relative in ("cmd/run.go", "dae/run.go", "dae/httpclient.go"):
            with self.subTest(source=relative):
                path = self.root / relative
                original = path.read_bytes()
                path.write_bytes(original + b"// unreviewed local edit\n")
                changed = path.read_bytes()
                with self.assertRaisesRegex(ValueError, "Unknown backend source"):
                    PREPARE.prepare(self.root)
                self.assertEqual(path.read_bytes(), changed)
                path.write_bytes(original)

    def test_unknown_protocol_wrapper_edit_is_preserved(self):
        PREPARE.prepare(self.root)
        path = self.root / "dae-core/component/outbound/dialer/router_protocol_compat.go"
        path.write_bytes(path.read_bytes() + b"// local transport change\n")
        before = path.read_bytes()
        with self.assertRaisesRegex(ValueError, "Unknown protocol compatibility source"):
            PREPARE.prepare(self.root)
        self.assertEqual(path.read_bytes(), before)

    def test_previously_prepared_runtime_can_receive_reviewed_update(self):
        PREPARE.prepare(self.root)
        path = self.root / "dae/runtime.go"
        path.write_bytes(path.read_bytes() + b"// previous reviewed release\n")
        manifest = self.root / ".daed-openwrt-prepare.json"
        state = json.loads(manifest.read_text(encoding="utf-8"))
        state["dae/runtime.go"] = hashlib.sha256(path.read_bytes()).hexdigest()
        manifest.write_text(json.dumps(state), encoding="utf-8")
        PREPARE.prepare(self.root)
        self.assertEqual(path.read_bytes(), (ROOT / "openwrt/wing/runtime.go").read_bytes())

    def test_unknown_connection_source_is_preserved(self):
        PREPARE.prepare(self.root)
        for relative in ("dae-core/control/dial.go", "dae-core/control/dns_control.go", "dae-core/control/router_connection.go"):
            with self.subTest(source=relative):
                path = self.root / relative
                original = path.read_bytes()
                path.write_bytes(original + b"// unreviewed local edit\n")
                before = {str(p.relative_to(self.root)): p.read_bytes() for p in self.root.rglob("*") if p.is_file()}
                with self.assertRaises(ValueError):
                    PREPARE.prepare(self.root)
                after = {str(p.relative_to(self.root)): p.read_bytes() for p in self.root.rglob("*") if p.is_file()}
                self.assertEqual(before, after)
                path.write_bytes(original)


if __name__ == "__main__":
    unittest.main()
