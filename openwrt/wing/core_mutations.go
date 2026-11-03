// SPDX-License-Identifier: AGPL-3.0-only
package graphql

import (
	"context"
	"encoding/json"
	"github.com/daeuniverse/dae-wing/common"
	"github.com/daeuniverse/dae-wing/dae"
	"github.com/daeuniverse/dae-wing/db"
)

func (r *queryResolver) CoreStatus() string { return dae.RouterCoreStatus() }
func (r *queryResolver) ConnectivityStatus(ctx context.Context, args struct{ Refresh, Force *bool }) string {
	return dae.RouterConnectivityStatus(ctx, args.Refresh != nil && *args.Refresh, args.Force != nil && *args.Force)
}
func (r *queryResolver) CoreRuntimeGroups(ctx context.Context) string {
	var nodes []db.Node
	ids := map[string]string{}
	if db.DB(ctx).Select("id", "link").Find(&nodes).Error == nil {
		for _, node := range nodes {
			if _, duplicate := ids[node.Link]; duplicate {
				ids[node.Link] = ""
			} else {
				ids[node.Link] = string(common.EncodeCursor(node.ID))
			}
		}
	}
	return dae.RouterCoreGroups(ctx, ids)
}
func (r *queryResolver) CoreCapabilities() string { return dae.RouterCoreCapabilities() }
func (r *queryResolver) CorePlan(ctx context.Context, args struct {
	Target string
	Mode   *string
}) string {
	var mode []string
	if args.Mode != nil {
		mode = []string{*args.Mode}
	}
	data, _ := json.Marshal(dae.RouterCorePlan(ctx, args.Target, mode...))
	return string(data)
}
func (r *MutationResolver) SwitchCore(ctx context.Context, args struct {
	Target, Revision string
	Operation        *string
	Mode             *string
}) (int32, error) {
	op := ""
	if args.Operation != nil {
		op = *args.Operation
	}
	mode := ""
	if args.Mode != nil {
		mode = *args.Mode
	}
	if err := dae.SwitchRouterCore(ctx, args.Target, args.Revision, op, mode); err != nil {
		return 0, err
	}
	return 1, nil
}
