package routercore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedVersionReceiptIsBoundToExecutable(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "sing-box")
	os.WriteFile(binary, []byte("binary-one"), 0700)
	if v, e := ExpectedCoreVersion(binary, "sing-box"); e != nil || v != SingBoxVersion {
		t.Fatal(v, e)
	}
	digest, _ := BinarySHA256(binary)
	data, _ := json.Marshal(CoreReceipt{Engine: "sing-box", Version: "1.14.4-nailink.1", SHA256: digest, MinAdapter: AdapterRevision, MaxAdapter: AdapterRevision})
	if err := Write(ReceiptPath(binary), data); err != nil {
		t.Fatal("write version receipt", err)
	}
	if v, e := ExpectedCoreVersion(binary, "sing-box"); e != nil || v != "1.14.4-nailink.1" {
		t.Fatal(v, e)
	}
	identity := CoreBinaryIdentity(binary)
	os.WriteFile(binary, []byte("binary-two"), 0700)
	if _, e := ExpectedCoreVersion(binary, "sing-box"); e == nil {
		t.Fatal("accepted different executable")
	}
	if CoreBinaryIdentity(binary) == identity {
		t.Fatal("compatibility identity did not change")
	}
	os.Remove(ReceiptPath(binary))
	os.Symlink(filepath.Join(root, "other"), ReceiptPath(binary))
	if _, e := ExpectedCoreVersion(binary, "sing-box"); e == nil {
		t.Fatal("followed substituted receipt")
	}
}
func TestCorePublishLockExcludesStartup(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "core")
	exclusive, e := LockBinary(binary, true)
	if e != nil {
		t.Fatal(e)
	}
	if shared, e := LockBinary(binary, false); e == nil {
		shared.Close()
		t.Fatal("started during publication")
	}
	exclusive.Close()
	shared, e := LockBinary(binary, false)
	if e != nil {
		t.Fatal(e)
	}
	defer shared.Close()
	if other, e := LockBinary(binary, false); e != nil {
		t.Fatal(e)
	} else {
		other.Close()
	}
	if writer, e := LockBinary(binary, true); e == nil {
		writer.Close()
		t.Fatal("published during startup")
	}
}

func TestMihomoLegacyAndReviewedReceiptsRemainDistinct(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "mihomo")
	if err := os.WriteFile(binary, []byte("test-binary"), 0700); err != nil {
		t.Fatal(err)
	}
	if v, err := ExpectedCoreVersion(binary, "mihomo"); err != nil || v != "1.19.33" {
		t.Fatal("legacy installations without receipts changed", v, err)
	}
	digest, _ := BinarySHA256(binary)
	data, _ := json.Marshal(CoreReceipt{Engine: "mihomo", Version: ReviewedMihomoVersion, SHA256: digest, MinAdapter: AdapterRevision, MaxAdapter: AdapterRevision})
	if err := Write(ReceiptPath(binary), data); err != nil {
		t.Fatal(err)
	}
	if v, err := ExpectedCoreVersion(binary, "mihomo"); err != nil || v != ReviewedMihomoVersion {
		t.Fatal("reviewed version receipt not honored", v, err)
	}
}
