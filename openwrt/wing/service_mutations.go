// SPDX-License-Identifier: AGPL-3.0-only
package graphql

import (
	"context"
	"github.com/daeuniverse/dae-wing/graphql/service/config"
	"github.com/daeuniverse/dae-wing/graphql/service/node"
	"github.com/graph-gophers/graphql-go"
)

func (r *MutationResolver) StartServiceTest(ctx context.Context, args struct {
	IDs                []graphql.ID
	Operation, Profile string
	Force              *bool
}) (string, error) {
	return node.StartServiceJob(ctx, args.IDs, args.Operation, args.Profile, args.Force != nil && *args.Force)
}
func (r *MutationResolver) StopServiceTest(args struct{ Operation string }) (int32, error) {
	return node.CancelServiceJob(args.Operation)
}
func (r *queryResolver) ServiceResults(ctx context.Context, args struct {
	IDs     []graphql.ID
	Profile string
}) (string, error) { return node.ServiceResults(ctx, args.IDs, args.Profile) }
func (r *queryResolver) GroupServices(ctx context.Context) (string, error) {
	return config.GroupServices(ctx)
}
func (r *MutationResolver) SetGroupService(ctx context.Context, args struct {
	ID      graphql.ID
	Profile string
}) (int32, error) { return config.SetGroupService(ctx, args.ID, args.Profile) }
func (r *MutationResolver) EnsureServicePresets(ctx context.Context, args struct {
	Proxy  graphql.ID
	Name   string
	Select bool
}) (string, error) { return config.EnsureServicePresets(ctx, args.Proxy, args.Name, args.Select) }
