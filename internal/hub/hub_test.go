package hub

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func TestRegisterAndGet(t *testing.T) {
	h := &Hub{
		conns:        make(map[string]*Conn),
		pending:      make(map[string]*pendingReq),
		creds:        newCredentialStore(""),
		pairingStore: newPairingStore(),
	}

	send := make(chan []byte, 1)
	h.Register("dev-1", send)

	c := h.Get("dev-1")
	if c == nil {
		t.Fatal("expected connection for dev-1")
	}
	if c.DeviceID != "dev-1" {
		t.Fatalf("expected device_id=dev-1, got %s", c.DeviceID)
	}
}

func TestRegisterReplacesOld(t *testing.T) {
	h := &Hub{
		conns:        make(map[string]*Conn),
		pending:      make(map[string]*pendingReq),
		creds:        newCredentialStore(""),
		pairingStore: newPairingStore(),
	}

	old := make(chan []byte, 1)
	h.Register("dev-1", old)

	newCh := make(chan []byte, 1)
	h.Register("dev-1", newCh)

	// Old channel should be closed
	select {
	case _, ok := <-old:
		if ok {
			t.Fatal("expected old channel to be closed")
		}
	default:
		t.Fatal("old channel should be closed and readable")
	}
}

func TestUnregister(t *testing.T) {
	h := &Hub{
		conns:        make(map[string]*Conn),
		pending:      make(map[string]*pendingReq),
		creds:        newCredentialStore(""),
		pairingStore: newPairingStore(),
	}

	send := make(chan []byte, 1)
	h.Register("dev-1", send)
	h.Unregister("dev-1")

	if h.Get("dev-1") != nil {
		t.Fatal("expected nil after unregister")
	}

	// Channel should be closed
	select {
	case _, ok := <-send:
		if ok {
			t.Fatal("expected channel to be closed")
		}
	default:
		t.Fatal("channel should be closed")
	}
}

func TestUnregisterCancelsPending(t *testing.T) {
	h := &Hub{
		conns:        make(map[string]*Conn),
		pending:      make(map[string]*pendingReq),
		creds:        newCredentialStore(""),
		pairingStore: newPairingStore(),
	}

	send := make(chan []byte, 16)
	h.Register("dev-1", send)

	// Create a pending request manually
	ch := make(chan []byte, 1)
	h.pendingMu.Lock()
	h.pending["dev-1|42"] = &pendingReq{
		ch:    ch,
		timer: time.AfterFunc(time.Hour, func() {}),
	}
	h.pendingMu.Unlock()

	h.Unregister("dev-1")

	// Pending channel should be closed
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("pending channel should be closed")
		}
	default:
		t.Fatal("pending channel should be closed and readable")
	}

	h.pendingMu.Lock()
	if _, exists := h.pending["dev-1|42"]; exists {
		t.Fatal("pending entry should be removed")
	}
	h.pendingMu.Unlock()
}

func TestListDeviceIDs(t *testing.T) {
	h := &Hub{
		conns:        make(map[string]*Conn),
		pending:      make(map[string]*pendingReq),
		creds:        newCredentialStore(""),
		pairingStore: newPairingStore(),
	}

	h.Register("a", make(chan []byte, 1))
	h.Register("b", make(chan []byte, 1))
	h.Register("c", make(chan []byte, 1))

	ids := h.ListDeviceIDs()
	if len(ids) != 3 {
		t.Fatalf("expected 3 devices, got %d", len(ids))
	}
}

func TestForwardAndDeliver(t *testing.T) {
	h := &Hub{
		conns:        make(map[string]*Conn),
		pending:      make(map[string]*pendingReq),
		creds:        newCredentialStore(""),
		pairingStore: newPairingStore(),
	}

	send := make(chan []byte, 16)
	h.Register("dev-1", send)

	req := `{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{}}`
	resp := `{"jsonrpc":"2.0","id":7,"result":{"tools":[]}}`

	var wg sync.WaitGroup
	wg.Add(1)

	var got []byte
	var gotOK bool

	go func() {
		defer wg.Done()
		got, gotOK = h.ForwardRequest("dev-1", []byte(req), 5*time.Second)
	}()

	// Simulate device: read from send channel, deliver response
	msg := <-send
	var parsed struct {
		ID interface{} `json:"id"`
	}
	if err := json.Unmarshal(msg, &parsed); err != nil {
		t.Fatal(err)
	}

	h.DeliverResponse("dev-1", []byte(resp))
	wg.Wait()

	if !gotOK {
		t.Fatal("expected ForwardRequest to succeed")
	}
	if string(got) != resp {
		t.Fatalf("expected response %q, got %q", resp, string(got))
	}
}

func TestForwardRequestNoDevice(t *testing.T) {
	h := &Hub{
		conns:        make(map[string]*Conn),
		pending:      make(map[string]*pendingReq),
		creds:        newCredentialStore(""),
		pairingStore: newPairingStore(),
	}

	req := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`
	_, ok := h.ForwardRequest("nonexistent", []byte(req), time.Second)
	if ok {
		t.Fatal("expected ForwardRequest to fail for missing device")
	}
}

func TestForwardRequestTimeout(t *testing.T) {
	h := &Hub{
		conns:        make(map[string]*Conn),
		pending:      make(map[string]*pendingReq),
		creds:        newCredentialStore(""),
		pairingStore: newPairingStore(),
	}

	send := make(chan []byte, 16)
	h.Register("dev-1", send)

	req := `{"jsonrpc":"2.0","id":99,"method":"slow","params":{}}`
	_, ok := h.ForwardRequest("dev-1", []byte(req), 100*time.Millisecond)
	if ok {
		t.Fatal("expected timeout")
	}
}

func TestForwardRequestDuplicateID(t *testing.T) {
	h := &Hub{
		conns:        make(map[string]*Conn),
		pending:      make(map[string]*pendingReq),
		creds:        newCredentialStore(""),
		pairingStore: newPairingStore(),
	}

	send := make(chan []byte, 16)
	h.Register("dev-1", send)

	req := `{"jsonrpc":"2.0","id":1,"method":"test","params":{}}`

	// First request: will block waiting for response
	go func() {
		h.ForwardRequest("dev-1", []byte(req), 5*time.Second)
	}()
	// Drain the forwarded message
	<-send

	// Second request with same id should fail (duplicate)
	_, ok := h.ForwardRequest("dev-1", []byte(req), time.Second)
	if ok {
		t.Fatal("expected duplicate request ID to fail")
	}
}
