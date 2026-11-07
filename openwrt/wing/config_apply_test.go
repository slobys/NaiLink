// SPDX-License-Identifier: AGPL-3.0-only
package config

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/daeuniverse/dae-wing/common"
	"github.com/daeuniverse/dae-wing/dae"
	"github.com/daeuniverse/dae-wing/db"
	"github.com/daeuniverse/dae-wing/graphql/service/general"
	"github.com/daeuniverse/dae-wing/graphql/service/group"
	"github.com/daeuniverse/dae/component/outbound"
	daeConfig "github.com/daeuniverse/dae/config"
	"github.com/daeuniverse/dae/pkg/config_parser"
	"github.com/graph-gophers/graphql-go"
	"github.com/sirupsen/logrus"
)

func TestApplyReleasesSnapshotBeforeCoreWait(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		result error
	}{{"success", nil}, {"rollback", io.EOF}} {
		t.Run(fixture.name, func(t *testing.T) { testConcurrentApply(t, fixture.result) })
	}
}

func testConcurrentApply(t *testing.T, reloadError error) {
	t.Helper()
	if err := db.InitDatabase(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	db.SetOutput(io.Discard)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d := db.DB(ctx)
	for _, model := range []interface{}{
		&db.Config{ID: 1, Name: "fixture", Global: "global{}", Selected: true},
		&db.Dns{ID: 1, Name: "fixture", Dns: "dns{}", Selected: true},
		&db.Routing{ID: 1, Name: "fixture", Routing: "routing{fallback:proxy}", Selected: true},
		&db.Group{ID: 1, Name: "proxy", Policy: "random"},
		&db.Node{ID: 1, Name: "fixture", Link: "vless://33333333-3333-4333-8333-333333333333@192.0.2.1:443#fixture"},
		&db.System{Running: true},
	} {
		if err := d.Create(model).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := group.AddNodes(ctx, common.EncodeCursor(1), []graphql.ID{common.EncodeCursor(1)}); err != nil {
		t.Fatal(err)
	}
	var captured db.Group
	if err := d.First(&captured, 1).Error; err != nil {
		t.Fatal(err)
	}
	previous := dae.ChReloadConfigs
	dae.ChReloadConfigs = make(chan *dae.ReloadMessage)
	defer func() { dae.ChReloadConfigs = previous }()
	applied := make(chan error, 1)
	go func() {
		_, err := Run(d, false)
		applied <- err
	}()
	var message *dae.ReloadMessage
	select {
	case message = <-dae.ChReloadConfigs:
	case err := <-applied:
		t.Fatalf("prepare failed: %v", err)
	case <-ctx.Done():
		t.Fatal("apply never reached reload")
	}
	if _, err := Run(d, true); err == nil {
		t.Error("second apply accepted before first acknowledgement")
	}
	removed := make(chan error, 1)
	go func() {
		_, err := group.DelNodes(ctx, common.EncodeCursor(1), []graphql.ID{common.EncodeCursor(1)})
		removed <- err
	}()
	select {
	case err := <-removed:
		if err != nil {
			t.Errorf("edit blocked by apply snapshot: %v", err)
		}
	case <-time.After(time.Second):
		t.Error("node removal waits for core reload while apply holds a read transaction")
	}
	message.Callback <- reloadError
	select {
	case err := <-applied:
		if !errors.Is(err, reloadError) {
			t.Errorf("apply result=%v, expected=%v", err, reloadError)
		}
	case <-ctx.Done():
		t.Error("apply did not finish after core replied")
	}
	var sys db.System
	var current db.Group
	if err := d.First(&sys).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.First(&current, 1).Error; err != nil {
		t.Fatal(err)
	}
	if current.Version != captured.Version+1 {
		t.Fatal("later edit version was overwritten")
	}
	if reloadError == nil && sys.RunningGroupVersionSum != captured.Version {
		t.Fatal("running status no longer describes applied snapshot")
	}
	if reloadError != nil && sys.RunningConfigID != nil {
		t.Fatal("failed apply saved a running configuration")
	}
	var count int64
	if err := d.Model(&db.Node{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("node data changed", count, err)
	}
	if err := d.Table("group_nodes").Count(&count).Error; err != nil || count != 0 {
		t.Fatal("later removal was overwritten", count, err)
	}
}

func TestApplyReloadAcknowledgementSurvivesBrowserCancellation(t *testing.T) {
	previous := dae.ChReloadConfigs
	dae.ChReloadConfigs = make(chan *dae.ReloadMessage)
	defer func() { dae.ChReloadConfigs = previous }()
	request, cancel := context.WithCancel(context.Background())
	defer cancel()
	operation, stop := routerApplyContext(request)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- routerApplyReload(request, operation, dae.EmptyConfig) }()
	message := <-dae.ChReloadConfigs
	cancel()
	select {
	case err := <-done:
		t.Fatalf("accepted reload abandoned: %v", err)
	default:
	}
	message.Callback <- nil
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("accepted reload acknowledgement lost")
	}
}

func TestApplyLateReplyDoesNotBlockCoreManager(t *testing.T) {
	previous := dae.ChReloadConfigs
	dae.ChReloadConfigs = make(chan *dae.ReloadMessage)
	defer func() { dae.ChReloadConfigs = previous }()
	operation, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- routerApplyReload(context.Background(), operation, dae.EmptyConfig) }()
	message := <-dae.ChReloadConfigs
	cancel()
	if err := <-done; err == nil {
		t.Fatal("expired operation reported success")
	}
	select {
	case message.Callback <- nil:
	case <-time.After(time.Second):
		t.Fatal("late acknowledgement blocks core manager")
	}
}

func TestCanceledApplyDoesNotQueueReload(t *testing.T) {
	previous := dae.ChReloadConfigs
	dae.ChReloadConfigs = make(chan *dae.ReloadMessage, 1)
	defer func() { dae.ChReloadConfigs = previous }()
	request, cancel := context.WithCancel(context.Background())
	cancel()
	if err := routerApplyReload(request, context.Background(), dae.EmptyConfig); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(dae.ChReloadConfigs) != 0 {
		t.Fatal("canceled request queued a reload")
	}
}

func TestRecordingRunningGroupsNeverRecreatesDeletedGroup(t *testing.T) {
	if err := db.InitDatabase(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	d := db.DB(context.Background())
	captured := db.Group{ID: 1, Name: "deleted while applying", Policy: "random", Version: 3}
	if err := d.Create(&db.System{ID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.Create(&captured).Error; err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(&captured).Error; err != nil {
		t.Fatal(err)
	}
	if err := routerRecordRunningGroups(d, 1, []db.Group{captured}); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := d.Model(&db.Group{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("deleted group was recreated", count, err)
	}
}

type applyResultResolver struct{}

func (*applyResultResolver) Run(ctx context.Context, args struct{ Dry bool }) (int32, error) {
	return Run(db.DB(ctx), args.Dry)
}

func TestApplyUnconfirmedSurvivesGraphQL(t *testing.T) {
	previous := dae.ChReloadConfigs
	dae.ChReloadConfigs = make(chan *dae.ReloadMessage)
	defer func() { dae.ChReloadConfigs = previous }()
	// Expire only after acceptance, without waiting ten minutes or starting a core.
	operation, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- routerApplyReload(context.Background(), operation, dae.EmptyConfig) }()
	message := <-dae.ChReloadConfigs
	cancel()
	unconfirmed := <-result
	message.Callback <- nil // A late acknowledgement must remain nonblocking.
	if unconfirmed == nil {
		t.Fatal("missing unconfirmed result")
	}

	schema := graphql.MustParseSchema(`type Query { run(dry: Boolean!): Int! }`, &applyResultResolver{})
	for _, dry := range []bool{false, true} {
		t.Run(fmt.Sprint(dry), func(t *testing.T) {
			if err := db.InitDatabase(t.TempDir()); err != nil {
				t.Fatal(err)
			}
			db.SetOutput(io.Discard)
			ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			d := db.DB(ctx)
			for _, model := range []any{
				&db.Config{ID: 1, Name: "fixture", Global: "global{}", Selected: true},
				&db.Dns{ID: 1, Name: "fixture", Dns: "dns{}", Selected: true},
				&db.Routing{ID: 1, Name: "fixture", Routing: "routing{fallback:direct}", Selected: true},
			} {
				if err := d.Create(model).Error; err != nil {
					t.Fatal(err)
				}
			}
			go func() {
				select {
				case message := <-dae.ChReloadConfigs:
					message.Callback <- unconfirmed
				case <-ctx.Done():
				}
			}()
			response := schema.Exec(ctx, fmt.Sprintf(`{run(dry:%t)}`, dry), "", nil)
			if len(response.Errors) != 1 || response.Errors[0].Extensions["code"] != "configuration_result_unconfirmed" {
				t.Fatalf("accepted reload uncertainty lost across GraphQL: %+v", response.Errors)
			}
		})
	}
}

// Issue #15: subscription members still count when a member is added explicitly.
// A rejected snapshot must not remove the explicit selection or stop an older one.
func TestFixedSubscriptionApplyPersistence(t *testing.T) {
	var diagnostics bytes.Buffer
	log := logrus.StandardLogger()
	previousOutput := log.Out
	log.SetOutput(&diagnostics)
	defer log.SetOutput(previousOutput)

	if err := db.InitDatabase(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	db.SetOutput(io.Discard)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d := db.DB(ctx)
	for _, model := range []interface{}{
		&db.Config{ID: 1, Name: "fixture", Global: "global{}", Selected: true, Version: 1},
		&db.Dns{ID: 1, Name: "fixture", Dns: "dns{}", Selected: true},
		&db.Routing{ID: 1, Name: "fixture", Routing: "routing{fallback:proxy}", Selected: true},
		&db.Group{ID: 1, Name: "proxy", Policy: "min_moving_avg"},
		&db.Subscription{ID: 1, Link: "https://fixture.invalid/sub?token=synthetic-secret"},
		&db.System{},
	} {
		if err := d.Create(model).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 52; i++ {
		link := fmt.Sprintf("trojan-go://synthetic-password@192.0.2.%d:443#fixture-%d", i, i)
		if i > 42 {
			link = "vmess://" + base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf(`{"v":"2","ps":"fixture-%d","add":"192.0.2.%d","port":"443","id":"33333333-3333-4333-8333-333333333333","aid":"0","net":"tcp","type":"none","tls":"tls"}`, i, i)))
		}
		if i == 52 {
			link = "vless://33333333-3333-4333-8333-333333333333@192.0.2.52:443#fixture-52"
		}
		subID := uint(1)
		if err := d.Create(&db.Node{ID: uint(i), Name: fmt.Sprintf("fixture-%d", i), Link: link, SubscriptionID: &subID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	groupID, nodeID, subID := common.EncodeCursor(1), common.EncodeCursor(16), common.EncodeCursor(1)
	if _, err := group.AddSubscriptions(ctx, groupID, []graphql.ID{subID}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := group.AddNodes(ctx, groupID, []graphql.ID{nodeID}); err != nil {
		t.Fatal(err)
	}

	previous := dae.ChReloadConfigs
	dae.ChReloadConfigs = make(chan *dae.ReloadMessage)
	defer func() { dae.ChReloadConfigs = previous }()
	var loaded *daeConfig.Config
	var coreTrace strings.Builder
	coreTrace.WriteString("fixture: initial direct management core\n")
	apply := func(dry bool, reply error, wantReload bool) error {
		t.Helper()
		done := make(chan error, 1)
		go func() { _, err := Run(d, dry); done <- err }()
		select {
		case message := <-dae.ChReloadConfigs:
			fmt.Fprintf(&coreTrace, "fixture: reload requested, dry=%t\n", dry)
			if !wantReload {
				t.Error("invalid snapshot reached core")
			}
			if reply == nil {
				loaded = message.Config
			}
			message.Callback <- reply
			return <-done
		case err := <-done:
			if wantReload {
				t.Fatalf("no reload: %v", err)
			}
			return err
		case <-ctx.Done():
			t.Fatal("apply timed out")
			return ctx.Err()
		}
	}
	wantBindings := int64(1)
	check := func(wantNodes int64, running, modified bool, version uint) {
		t.Helper()
		var count int64
		if err := d.Table("group_nodes").Count(&count).Error; err != nil || count != wantNodes {
			t.Fatalf("group_nodes=%d err=%v", count, err)
		}
		if err := d.Model(&db.Node{}).Count(&count).Error; err != nil || count != 52 {
			t.Fatalf("nodes=%d err=%v", count, err)
		}
		if err := d.Table("group_subscriptions").Count(&count).Error; err != nil || count != wantBindings {
			t.Fatal("subscription binding changed", count, err)
		}
		if wantNodes == 1 {
			var selected []uint
			if err := d.Table("group_nodes").Pluck("node_id", &selected).Error; err != nil || len(selected) != 1 || selected[0] != 16 {
				t.Fatal("selected node changed", selected, err)
			}
		}
		var sys db.System
		if err := d.First(&sys).Error; err != nil {
			t.Fatal(err)
		}
		if sys.Running != running || sys.RunningGroupVersionSum != version {
			t.Fatalf("running=%v version=%d want=%v/%d", sys.Running, sys.RunningGroupVersionSum, running, version)
		}
		status := general.DaeResolver{Ctx: ctx}
		if got, err := status.Modified(); err != nil || got != modified {
			t.Fatalf("modified=%v err=%v", got, err)
		}
	}
	if err := apply(false, nil, true); err != nil {
		t.Fatal(err)
	}
	if len(loaded.Node) != 52 {
		t.Fatalf("expanded nodes=%d", len(loaded.Node))
	}
	check(1, true, false, 2)
	old := loaded
	oldCoreTrace := coreTrace.String()
	if _, err := group.SetPolicy(ctx, groupID, "fixed", []config_parser.Param{{Key: "", Val: "0"}}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := apply(false, nil, false); err == nil {
			t.Fatal("multi-node fixed accepted")
		} else {
			t.Logf("fixed with explicit member plus subscription: %v", err)
			var extended interface{ Extensions() map[string]interface{} }
			if !errors.As(err, &extended) || extended.Extensions()["code"] != "configuration_fixed_group_members" {
				t.Fatalf("missing safe diagnostic: %v", err)
			}
			if !strings.Contains(diagnostics.String(), "configuration_fixed_group_members") || strings.Contains(diagnostics.String(), "synthetic-secret") {
				t.Fatal("safe apply diagnostic missing or exposed subscription URL", diagnostics.String())
			}
		}
		check(1, true, true, 2)
		if loaded != old || coreTrace.String() != oldCoreTrace {
			t.Fatal("rejected edit replaced working core or produced a core reload")
		}
	}
	// Only this explicit removal clears group_nodes; apply does not.
	if _, err := group.DelNodes(ctx, groupID, []graphql.ID{nodeID}); err != nil {
		t.Fatal(err)
	}
	if err := apply(false, nil, false); err == nil {
		t.Fatal("subscription-only fixed accepted")
	}
	check(0, true, true, 2)
	if _, err := group.AddNodes(ctx, groupID, []graphql.ID{nodeID}); err != nil {
		t.Fatal(err)
	}
	if _, err := group.DelSubscriptions(ctx, groupID, []graphql.ID{subID}); err != nil {
		t.Fatal(err)
	}
	wantBindings = 0
	if err := apply(false, io.EOF, true); !errors.Is(err, io.EOF) {
		t.Fatalf("reload failure=%v", err)
	}
	check(1, true, true, 2)
	if loaded != old {
		t.Fatal("failed core reload lost previous snapshot")
	}
	if err := apply(false, nil, true); err != nil {
		t.Fatal(err)
	}
	check(1, true, false, 6)
	if len(loaded.Node) != 1 || len(loaded.Group) != 1 || !strings.Contains(string(loaded.Node[0]), "fixture-16") {
		t.Fatal("fixed snapshot is not the selected member")
	}
	policy, err := outbound.NewDialerSelectionPolicyFromGroupParam(&loaded.Group[0])
	if err != nil || policy.FixedIndex != 0 {
		t.Fatalf("fixed positional parameter invalid: %v", err)
	}
	var persisted db.Group
	if err := d.Preload("PolicyParams").First(&persisted, 1).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Policy != "fixed" || len(persisted.PolicyParams) != 1 || persisted.PolicyParams[0].Key != "" || persisted.PolicyParams[0].Value != "0" {
		t.Fatal("fixed policy was not preserved")
	}
	if err := apply(true, nil, true); err != nil {
		t.Fatal(err)
	}
	check(1, false, false, 6)
	if loaded != dae.EmptyConfig {
		t.Fatal("stop did not load inactive management configuration")
	}
	if err := apply(false, nil, true); err != nil {
		t.Fatal(err)
	}
	check(1, true, false, 6)
}

func TestFixedMembershipDiagnosticThroughGraphQL(t *testing.T) {
	for _, tc := range []struct {
		name           string
		filter         *string
		explicit, want int
	}{
		{"no-members", nil, 0, 0},
		{"multiple-explicit", nil, 2, 2},
		{"subscription-expanded", new(""), 0, 2},
		{"filter-one-and-duplicate-explicit", new("^fixture-1$"), 1, 1},
		{"filter-no-match", new("^absent$"), 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.InitDatabase(t.TempDir()); err != nil {
				t.Fatal(err)
			}
			db.SetOutput(io.Discard)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			d := db.DB(ctx)
			for _, model := range []any{
				&db.Config{ID: 1, Name: "fixture", Global: "global{}", Selected: true},
				&db.Dns{ID: 1, Name: "fixture", Dns: "dns{}", Selected: true},
				&db.Routing{ID: 1, Name: "fixture", Routing: "routing{fallback:private_group_name}", Selected: true},
				&db.Group{ID: 1, Name: "private_group_name", Policy: "fixed", PolicyParams: []db.GroupPolicyParam{{Key: "", Value: "0"}}},
				&db.Subscription{ID: 1, Link: "https://fixture.invalid/sub?token=synthetic-secret"},
				&db.System{},
			} {
				if err := d.Create(model).Error; err != nil {
					t.Fatal(err)
				}
			}
			for i := 1; i <= 2; i++ {
				sid := uint(1)
				n := db.Node{ID: uint(i), Name: fmt.Sprintf("fixture-%d", i), SubscriptionID: &sid, Link: fmt.Sprintf("trojan-go://synthetic-secret@192.0.2.%d:443", i)}
				if err := d.Create(&n).Error; err != nil {
					t.Fatal(err)
				}
				if i <= tc.explicit {
					if _, err := group.AddNodes(ctx, common.EncodeCursor(1), []graphql.ID{common.EncodeCursor(uint(i))}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tc.filter != nil {
				if _, err := group.AddSubscriptions(ctx, common.EncodeCursor(1), []graphql.ID{common.EncodeCursor(1)}, tc.filter); err != nil {
					t.Fatal(err)
				}
			}
			previous := dae.ChReloadConfigs
			dae.ChReloadConfigs = make(chan *dae.ReloadMessage)
			defer func() { dae.ChReloadConfigs = previous }()
			var diagnostic bytes.Buffer
			log := logrus.StandardLogger()
			previousOutput := log.Out
			log.SetOutput(&diagnostic)
			defer log.SetOutput(previousOutput)
			schema := graphql.MustParseSchema(`type Query { run(dry: Boolean!): Int! }`, &applyResultResolver{})
			done := make(chan *graphql.Response, 1)
			go func() { done <- schema.Exec(ctx, `{run(dry:false)}`, "", nil) }()
			var result *graphql.Response
			select {
			case msg := <-dae.ChReloadConfigs:
				if tc.want != 1 {
					t.Error("invalid group reached core")
				}
				msg.Callback <- nil
				result = <-done
			case result = <-done:
				if tc.want == 1 {
					t.Error("valid filtered group did not load")
				}
			case <-ctx.Done():
				t.Fatal("request blocked")
			}
			if tc.want == 1 {
				if len(result.Errors) != 0 {
					t.Fatal(result.Errors)
				}
			} else {
				if len(result.Errors) != 1 || result.Errors[0].Extensions["code"] != "configuration_fixed_group_members" || result.Errors[0].Extensions["nodeCount"] != tc.want {
					t.Fatalf("missing member-count diagnostic: %+v", result.Errors)
				}
				if !strings.Contains(diagnostic.String(), "configuration_fixed_group_members") {
					t.Fatal("apply failure missing from logs")
				}
				for _, secret := range []string{"synthetic-secret", "fixture.invalid", "private_group_name"} {
					if strings.Contains(result.Errors[0].Message+diagnostic.String(), secret) {
						t.Fatal("private data escaped safe diagnostic")
					}
				}
			}
		})
	}
}

func TestApplyDiagnosticsNeverLogRawErrors(t *testing.T) {
	var output bytes.Buffer
	log := logrus.StandardLogger()
	previous := log.Out
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	err := errors.New("invalid config https://fixture.invalid/?token=synthetic-secret")
	routerApplyResult(&err)
	if strings.Contains(output.String(), "fixture.invalid") || strings.Contains(output.String(), "synthetic-secret") || !strings.Contains(output.String(), "configuration_apply_failed") {
		t.Fatal("unsafe or missing generic diagnostic", output.String())
	}
}
