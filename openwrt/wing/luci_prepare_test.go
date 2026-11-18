// SPDX-License-Identifier: AGPL-3.0-only
package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/daeuniverse/dae-wing/db"
)

func TestPrepareLuCIIdentityPreservesExistingUserAndPreferences(t *testing.T) {
	directory := t.TempDir()
	if err := db.InitDatabase(directory); err != nil {
		t.Fatal(err)
	}
	old := db.User{Username: "existing", PasswordHash: "unchanged", JwtSecret: "existing-secret", JsonStorage: `{"mode":"rule","defaultConfigID":"42"}`}
	if err := db.DB(t.Context()).Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := prepareLuCIIdentity(t.Context(), directory); err != nil {
			t.Fatal(err)
		}
	}
	var users []db.User
	if err := db.DB(t.Context()).Find(&users).Error; err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].PasswordHash != old.PasswordHash || users[0].JsonStorage != old.JsonStorage {
		t.Fatalf("existing account changed: %d users", len(users))
	}
	value := readPreparedIdentity(t, directory)
	if value["username"] != old.Username || value["secret"] != old.JwtSecret {
		t.Fatal("existing identity was not preserved")
	}
}

func TestPrepareLuCIIdentityCreatesPrivateLocalAccount(t *testing.T) {
	directory := t.TempDir()
	if err := prepareLuCIIdentity(t.Context(), directory); err != nil {
		t.Fatal(err)
	}
	value := readPreparedIdentity(t, directory)
	if value["username"] != "luci" || len(value["secret"]) != 64 {
		t.Fatal("missing private identity")
	}
	var user db.User
	if err := db.DB(t.Context()).First(&user).Error; err != nil {
		t.Fatal(err)
	}
	if user.PasswordHash != "!" || user.JwtSecret != value["secret"] {
		t.Fatal("local identity must have a matching secret and no usable password")
	}
}

func readPreparedIdentity(t *testing.T, directory string) map[string]string {
	t.Helper()
	path := filepath.Join(directory, "luci-session.json")
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("local credential file must be private")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]string
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
