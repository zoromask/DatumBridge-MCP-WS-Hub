package hub

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegisterDeviceAndValidate(t *testing.T) {
	s := newCredentialStore("")

	cred, err := s.RegisterDevice("test-dev")
	if err != nil {
		t.Fatalf("RegisterDevice failed: %v", err)
	}
	if cred.DeviceID != "test-dev" {
		t.Fatalf("expected device_id=test-dev, got %s", cred.DeviceID)
	}
	if cred.Token == "" {
		t.Fatal("expected non-empty token")
	}

	if !s.ValidateToken("test-dev", cred.Token) {
		t.Fatal("expected token to validate")
	}
}

func TestValidateWrongToken(t *testing.T) {
	s := newCredentialStore("")

	cred, _ := s.RegisterDevice("dev-1")
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

	cred, _ := s.RegisterDevice("dev-1")
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

	cred, err := s.RegisterDevice("")
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

	// Register a device and persist
	s1 := newCredentialStore(fp)
	cred, err := s1.RegisterDevice("persist-dev")
	if err != nil {
		t.Fatalf("RegisterDevice failed: %v", err)
	}

	// Load from same file
	s2 := newCredentialStore(fp)
	if s2.Count() != 1 {
		t.Fatalf("expected 1 device after reload, got %d", s2.Count())
	}
	if !s2.ValidateToken("persist-dev", cred.Token) {
		t.Fatal("expected token to validate after reload")
	}
}

func TestPersistenceRevoke(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "creds.json")

	s := newCredentialStore(fp)
	s.RegisterDevice("dev-a")
	s.RegisterDevice("dev-b")
	s.RevokeDevice("dev-a")

	s2 := newCredentialStore(fp)
	if s2.Count() != 1 {
		t.Fatalf("expected 1 device after revoke+reload, got %d", s2.Count())
	}
}

func TestMigratePlainTextTokens(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "creds.json")

	// Write a plain-text token file (simulating old format)
	data := []byte(`{"old-device": "plain-text-token-value"}`)
	if err := os.WriteFile(fp, data, 0600); err != nil {
		t.Fatal(err)
	}

	// Load — should migrate to bcrypt
	s := newCredentialStore(fp)
	if s.Count() != 1 {
		t.Fatalf("expected 1 device, got %d", s.Count())
	}
	if !s.ValidateToken("old-device", "plain-text-token-value") {
		t.Fatal("expected migrated token to still validate with original value")
	}

	// Verify the stored value is now a bcrypt hash
	s.mu.RLock()
	stored := s.tokens["old-device"]
	s.mu.RUnlock()
	if !isBcryptHash(stored) {
		t.Fatalf("expected bcrypt hash after migration, got %s", stored)
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

	ps.Add(code, cred)

	// List should show 1 pending
	pending := ps.List()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending pairing, got %d", len(pending))
	}

	// Consume
	consumed, ok := ps.Consume(code)
	if !ok {
		t.Fatal("expected Consume to succeed")
	}
	if consumed.DeviceID != "pair-dev" {
		t.Fatalf("expected device_id=pair-dev, got %s", consumed.DeviceID)
	}

	// Second consume should fail
	_, ok = ps.Consume(code)
	if ok {
		t.Fatal("expected second Consume to fail")
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
