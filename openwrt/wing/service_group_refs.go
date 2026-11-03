// SPDX-License-Identifier: AGPL-3.0-only
package db

import (
	"gorm.io/gorm"
	"regexp"
	"strings"
)

// Group names are identifiers. Replace only complete routing destinations;
// never replace domains, comments, or a longer group's name.
func RenameGroupRoutingRefs(tx *gorm.DB, oldName, newName string) error {
	if oldName == newName {
		return nil
	}
	pattern := regexp.MustCompile(`(->[\t ]*|fallback:[\t ]*)` + regexp.QuoteMeta(oldName) + `([\t ]*}?[\t ]*$)`)
	var routes []Routing
	if err := tx.Find(&routes).Error; err != nil {
		return err
	}
	for _, r := range routes {
		lines := strings.Split(r.Routing, "\n")
		for i, line := range lines {
			code, comment, hasComment := strings.Cut(line, "#")
			code = pattern.ReplaceAllStringFunc(code, func(s string) string {
				indices := pattern.FindStringSubmatchIndex(s)
				return s[:indices[3]] + newName + s[indices[4]:]
			})
			if hasComment {
				code += "#" + comment
			}
			lines[i] = code
		}
		updated := strings.Join(lines, "\n")
		if updated != r.Routing {
			if err := tx.Model(&r).Updates(map[string]any{"routing": updated, "version": gorm.Expr("version + 1")}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
