// SPDX-License-Identifier: AGPL-3.0-only
package routercore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"syscall"
)

// An update receipt is published only after a verified managed download. Keep
// the original pinned distribution usable when there is no receipt.
type CoreReceipt struct {
	Engine     string `json:"engine"`
	Version    string `json:"version"`
	SHA256     string `json:"sha256"`
	MinAdapter int    `json:"min_adapter"`
	MaxAdapter int    `json:"max_adapter"`
}

var coreVersionPattern = regexp.MustCompile(`^[0-9]{1,5}\.[0-9]{1,5}\.[0-9]{1,5}(?:-nailink\.[0-9]{1,5})?$`)

func ValidCoreVersion(engine, version string) bool {
	return (engine == "sing-box" || engine == "mihomo") && coreVersionPattern.MatchString(version)
}
func ReceiptPath(binary string) string { return binary + ".version.json" }
func CoreBinaryIdentity(binary string) string {
	st, err := os.Lstat(binary)
	if err != nil {
		return "missing"
	}
	receipt, err := Read(ReceiptPath(binary), 4096)
	return Fingerprint([]any{st.Size(), st.ModTime().UnixNano(), st.Mode(), string(receipt), err != nil})
}
func LockBinary(binary string, exclusive bool) (*os.File, error) {
	f, err := os.OpenFile(binary+".update.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, errors.New("unsafe_core_lock")
	}
	operation := syscall.LOCK_SH
	if exclusive {
		operation = syscall.LOCK_EX
	}
	if err = syscall.Flock(int(f.Fd()), operation|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func BinarySHA256(binary string) (string, error) {
	st, err := os.Lstat(binary)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 || st.Size() <= 0 || st.Size() > 200<<20 {
		return "", errors.New("unsafe_core_binary")
	}
	f, err := os.Open(binary)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, io.LimitReader(f, (200<<20)+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func ExpectedCoreVersion(binary, engine string) (string, error) {
	data, err := Read(ReceiptPath(binary), 4096)
	if os.IsNotExist(err) {
		if engine == "sing-box" {
			return SingBoxVersion, nil
		}
		if engine == "mihomo" {
			return MihomoVersion, nil
		}
	}
	if err != nil {
		return "", err
	}
	var receipt CoreReceipt
	if json.Unmarshal(data, &receipt) != nil || receipt.Engine != engine || !ValidCoreVersion(engine, receipt.Version) || !ValidRevision(receipt.SHA256) {
		return "", errors.New("invalid_core_receipt")
	}
	if receipt.MinAdapter < 1 || receipt.MaxAdapter < receipt.MinAdapter || AdapterRevision < receipt.MinAdapter || AdapterRevision > receipt.MaxAdapter {
		return "", errors.New("invalid_core_receipt")
	}
	digest, err := BinarySHA256(binary)
	if err != nil || digest != receipt.SHA256 {
		return "", errors.New("invalid_core_receipt")
	}
	return receipt.Version, nil
}
