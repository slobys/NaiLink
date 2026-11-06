// SPDX-License-Identifier: AGPL-3.0-only
package config

import (
	"context"
	"errors"
	"time"

	"github.com/daeuniverse/dae-wing/dae"
	"github.com/daeuniverse/dae-wing/db"
	daeConfig "github.com/daeuniverse/dae/config"
	"gorm.io/gorm"
)

var errRouterApplyUnconfirmed = errors.New("应用结果暂未确认，请重新读取状态并查看日志；节点与配置保留，请勿重复提交")

type routerApplyUnconfirmedError struct{ error }

func (e routerApplyUnconfirmedError) Unwrap() error { return e.error }
func (routerApplyUnconfirmedError) Extensions() map[string]interface{} {
	return map[string]interface{}{"code": "configuration_result_unconfirmed"}
}

// Run wraps reload errors with operation context. GraphQL only reads extensions
// on the outer error, so retain the uncertainty code after those wrappers.
func routerApplyResult(err *error) {
	if errors.Is(*err, errRouterApplyUnconfirmed) {
		*err = routerApplyUnconfirmedError{*err}
	}
}

// Once a reload is accepted, finish its acknowledgement and status write even
// if its browser has disconnected. This bounded lifetime includes Tor startup
// and rollback; no database connection is held while waiting for the core.
func routerApplyContext(request context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(request), 10*time.Minute)
}

// Association.Replace can upsert a group that was deleted during the reload.
// Only attach rows still present; captured IDs/versions keep those edits pending.
func routerRecordRunningGroups(d *gorm.DB, system uint, groups []db.Group) error {
	if err := d.Model(&db.Group{}).Where("system_id = ?", system).Update("system_id", nil).Error; err != nil {
		return err
	}
	ids := make([]uint, 0, len(groups))
	for _, g := range groups {
		ids = append(ids, g.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	return d.Model(&db.Group{}).Where("id IN ?", ids).Update("system_id", system).Error
}

func routerApplyReload(request, operation context.Context, conf *daeConfig.Config) error {
	if err := request.Err(); err != nil {
		return err
	}
	callback := make(chan error, 1)
	message := &dae.ReloadMessage{Config: conf, Callback: callback}
	select {
	case dae.ChReloadConfigs <- message:
	case <-request.Done():
		return request.Err()
	case <-operation.Done():
		return operation.Err()
	}
	select {
	case err := <-callback:
		return err
	case <-operation.Done():
		// A late reply must never wedge the core-manager receive loop.
		return errRouterApplyUnconfirmed
	}
}
