// SPDX-License-Identifier: AGPL-3.0-only
package graphql

import (
	"context"
	"encoding/json"
	"github.com/daeuniverse/dae-wing/dae"
)

func (r *queryResolver) CoreStatus() string { return dae.RouterCoreStatus() }
func (r *queryResolver) CoreRuntimeGroups(ctx context.Context) string {
	return dae.RouterCoreGroups(ctx)
}
func (r *queryResolver) CoreCapabilities() string { return dae.RouterCoreCapabilities() }
func (r *queryResolver) CorePlan(ctx context.Context, args struct{ Target string }) string {
	data, _ := json.Marshal(dae.RouterCorePlan(ctx, args.Target))
	return string(data)
}
func (r *MutationResolver) SwitchCore(ctx context.Context, args struct {
	Target, Revision string
	Operation        *string
}) (int32, error) {
	op := ""
	if args.Operation != nil {
		op = *args.Operation
	}
	if err := dae.SwitchRouterCore(ctx, args.Target, args.Revision, op); err != nil {
		return 0, err
	}
	return 1, nil
}
