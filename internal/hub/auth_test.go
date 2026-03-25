package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRegisterDeviceAndValidate(t *testing.T) {
	s := newCredentialStore("")

	cred, err := s.RegisterDevice("test-dev", "TestPC", "10.0.0.1", "AA:BB:CC:DD:EE:FF")
	if err != nil {
		t.Fatalf("RegisterDevice failed: %v", err)
	}
	if cred.DeviceID != "test-dev" {
		t.Fatalf("expected device_id=test-dev, got %s", cred.DeviceID)
	}
	if cred.Token == "" {
		t.Fatal("expected non-empty token")
	}
	if cred.DeviceName != "TestPC" {
		t.Fatalf("expected device_name=TestPC, got %s", cred.DeviceName)
	}

	if !s.ValidateToken("test-dev", cred.Token) {
		t.Fatal("expected token to validate")
	}
}

func TestValidateWrongToken(t *testing.T) {
	s := newCredentialStore("")

	cred, _ := s.RegisterDevice("dev-1", "", "", "")
	if s.ValidateToken("dev-1", cred.Token+"wrong") {
		t.Fatal("expected wrong token to fail validation")
	}
}

func TestValidateNonexistentDevice(t *testing.T) {
	s := newCredentialStore("")
	if s.ValidateToken("nonexistent", "sometoken") {
		t.Fatal("expected nonexistent device to fail validation")
	}
}

func TestRevokeDevice(t *testing.T) {
	s := newCredentialStore("")

	cred, _ := s.RegisterDevice("dev-1", "", "", "")
	s.RevokeDevice("dev-1")

	if s.ValidateToken("dev-1", cred.Token) {
		t.Fatal("expected revoked device to fail validation")
	}
	if s.Count() != 0 {
		t.Fatalf("expected 0 devices, got %d", s.Count())
	}
}

func TestRegisterDeviceAutoID(t *testing.T) {
	s := newCredentialStore("")

	cred, err := s.RegisterDevice("", "", "", "")
	if err != nil {
		t.Fatalf("RegisterDevice failed: %v", err)
	}
	if cred.DeviceID == "" {
		t.Fatal("expected auto-generated device_id")
	}
	if len(cred.DeviceID) < 10 {
		t.Fatalf("auto-generated device_id too short: %s", cred.DeviceID)
	}
}

func TestPersistence(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "creds.json")

	s1 := newCredentialStore(fp)
	cred, err := s1.RegisterDevice("persist-dev", "MyLaptop", "192.168.1.5", "11:22:33:44:55:66")
	if err != nil {
		t.Fatalf("RegisterDevice failed: %v", err)
	}

	s2 := newCredentialStore(fp)
	if s2.Count() != 1 {
		t.Fatalf("expected 1 device after reload, got %d", s2.Count())
	}
	if !s2.ValidateToken("persist-dev", cred.Token) {
		t.Fatal("expected token to validate after reload")
	}

	rec := s2.GetDeviceRecord("persist-dev")
	if rec == nil {
		t.Fatal("expected device record")
	}
	if rec.DeviceName != "MyLaptop" {
		t.Fatalf("expected device_name=MyLaptop, got %s", rec.DeviceName)
	}
	if rec.DeviceIP != "192.168.1.5" {
		t.Fatalf("expected device_ip=192.168.1.5, got %s", rec.DeviceIP)
	}
	if rec.DeviceMAC != "11:22:33:44:55:66" {
		t.Fatalf("expected device_mac=11:22:33:44:55:66, got %s", rec.DeviceMAC)
	}
}

func TestPersistenceRevoke(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "creds.json")

	s := newCredentialStore(fp)
	s.RegisterDevice("dev-a", "", "", "")
	s.RegisterDevice("dev-b", "", "", "")
	s.RevokeDevice("dev-a")

	s2 := newCredentialStore(fp)
	if s2.Count() != 1 {
		t.Fatalf("expected 1 device after revoke+reload, got %d", s2.Count())
	}
}

func TestMigratePlainTextTokens(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "creds.json")

	// Old format: map[string]string (device_id -> plain token)
	data := []byte(`{"old-device": "plain-text-token-value"}`)
	if err := os.WriteFile(fp, data, 0600); err != nil {
		t.Fatal(err)
	}

	s := newCredentialStore(fp)
	if s.Count() != 1 {
		t.Fatalf("expected 1 device, got %d", s.Count())
	}
	if !s.ValidateToken("old-device", "plain-text-token-value") {
		t.Fatal("expected migrated token to still validate with original value")
	}

	rec := s.GetDeviceRecord("old-device")
	if rec == nil {
		t.Fatal("expected device record after migration")
	}
	if !isBcryptHash(rec.TokenHash) {
		t.Fatalf("expected bcrypt hash after migration, got %s", rec.TokenHash)
	}
}

func TestNewFormatPersistence(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "creds.json")

	s1 := newCredentialStore(fp)
	s1.RegisterDevice("dev-new", "Desktop-PC", "10.10.10.1", "AA:11:BB:22:CC:33")

	// Read raw file to verify format
	raw, err := os.ReadFile(fp)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]*deviceRecord
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("expected new format JSON, got error: %v", err)
	}
	rec := parsed["dev-new"]
	if rec == nil {
		t.Fatal("expected dev-new in file")
	}
	if rec.DeviceName != "Desktop-PC" {
		t.Fatalf("expected device_name=Desktop-PC in file, got %s", rec.DeviceName)
	}
	if !isBcryptHash(rec.TokenHash) {
		t.Fatalf("expected bcrypt hash, got %s", rec.TokenHash)
	}
}

func TestPairingFlow(t *testing.T) {
	s := newCredentialStore("")
	ps := newPairingStore()

	code, cred, err := s.StartPairing("pair-dev")
	if err != nil {
		t.Fatalf("StartPairing failed: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("expected 6-digit code, got %q", code)
	}
	if cred.DeviceID != "pair-dev" {
		t.Fatalf("expected device_id=pair-dev, got %s", cred.DeviceID)
	}

	ps.Add(code, cred, time.Now().Add(5*time.Minute))

	pending := ps.List()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending pairing, got %d", len(pending))
	}
	if pending[0].DeviceID != "pair-dev" {
		t.Fatalf("expected pending device_id=pair-dev, got %q", pending[0].DeviceID)
	}
	if pending[0].PairingCode != code {
		t.Fatalf("expected pending pairing_code=%s, got %q", code, pending[0].PairingCode)
	}

	consumed, ok := ps.Consume(code)
	if !ok {
		t.Fatal("expected Consume to succeed")
	}
	if consumed.DeviceID != "pair-dev" {
		t.Fatalf("expected device_id=pair-dev, got %s", consumed.DeviceID)
	}

	_, ok = ps.Consume(code)
	if ok {
		t.Fatal("expected second Consume to fail")
	}
}

func TestPairingExpiredDroppedFromList(t *testing.T) {
	ps := newPairingStore()
	ps.Add("000000", &DeviceCredential{DeviceID: "gone", Token: "t"}, time.Now().Add(-time.Second))
	if len(ps.List()) != 0 {
		t.Fatalf("expected expired pairing pruned, got %d entries", len(ps.List()))
	}
}

func TestPairingWithMetadata(t *testing.T) {
	s := newCredentialStore("")
	ps := newPairingStore()

	code, cred, err := s.StartPairing("meta-dev")
	if err != nil {
		t.Fatal(err)
	}

	ps.Add(code, cred, time.Now().Add(5*time.Minute))
	consumed, ok := ps.Consume(code)
	if !ok {
		t.Fatal("expected Consume to succeed")
	}

	consumed.DeviceName = "TestMachine"
	consumed.DeviceIP = "192.168.0.100"
	consumed.DeviceMAC = "FF:EE:DD:CC:BB:AA"

	if err := s.AddCredential(consumed); err != nil {
		t.Fatal(err)
	}

	rec := s.GetDeviceRecord("meta-dev")
	if rec == nil {
		t.Fatal("expected record")
	}
	if rec.DeviceName != "TestMachine" {
		t.Fatalf("expected TestMachine, got %s", rec.DeviceName)
	}
	if rec.DeviceIP != "192.168.0.100" {
		t.Fatalf("expected 192.168.0.100, got %s", rec.DeviceIP)
	}
	if rec.DeviceMAC != "FF:EE:DD:CC:BB:AA" {
		t.Fatalf("expected FF:EE:DD:CC:BB:AA, got %s", rec.DeviceMAC)
	}
}

func TestPairingInvalidCode(t *testing.T) {
	ps := newPairingStore()

	_, ok := ps.Consume("000000")
	if ok {
		t.Fatal("expected Consume of nonexistent code to fail")
	}
}

func TestGenerateToken(t *testing.T) {
	t1, err := generateToken()
	if err != nil {
		t.Fatalf("generateToken failed: %v", err)
	}
	if len(t1) != 64 {
		t.Fatalf("expected 64-char hex token, got %d chars", len(t1))
	}

	t2, _ := generateToken()
	if t1 == t2 {
		t.Fatal("expected unique tokens")
	}
}

func TestGeneratePairingCode(t *testing.T) {
	code, err := generatePairingCode()
	if err != nil {
		t.Fatalf("generatePairingCode failed: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("expected 6-digit code, got %q", code)
	}
}
