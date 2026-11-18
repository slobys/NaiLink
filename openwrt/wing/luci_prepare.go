// SPDX-License-Identifier: AGPL-3.0-only
package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/daeuniverse/dae-wing/db"
	"github.com/spf13/cobra"
)

func init() {
	var directory string
	command := &cobra.Command{
		Use:          "prepare-luci",
		Short:        "Prepare the private LuCI identity before starting NaiLink",
		Hidden:       true,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE:         func(cmd *cobra.Command, _ []string) error { return prepareLuCIIdentity(cmd.Context(), directory) },
	}
	command.Flags().StringVar(&directory, "config", "/etc/daed", "NaiLink configuration directory")
	rootCmd.AddCommand(command)
}

// Run with the service stopped. Reuse the existing identity and preferences.
func prepareLuCIIdentity(ctx context.Context, directory string) error {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	if err := db.InitDatabase(directory); err != nil {
		return err
	}
	var users []db.User
	if err := db.DB(ctx).Order("id").Limit(1).Find(&users).Error; err != nil {
		return err
	}
	var user db.User
	if len(users) > 0 {
		user = users[0]
	} else {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return err
		}
		user = db.User{Username: "luci", PasswordHash: "!", JwtSecret: hex.EncodeToString(secret), JsonStorage: "{}"}
		if err := db.DB(ctx).Create(&user).Error; err != nil {
			return errors.New("cannot create local LuCI identity")
		}
	}
	if user.Username == "" || user.JwtSecret == "" {
		return errors.New("invalid local identity")
	}
	file, err := os.CreateTemp(directory, ".luci-session-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	value := map[string]string{"username": user.Username, "secret": user.JwtSecret}
	if err = json.NewEncoder(file).Encode(value); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(directory, "luci-session.json"))
}
