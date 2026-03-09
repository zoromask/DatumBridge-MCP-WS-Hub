package hub

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func newTestHub() *Hub {
	return &Hub{
		conns:        make(map[string]*Conn),
		pending:      make(map[string]*pendingReq),
		creds:        newCredentialStore(""),
		pairingStore: newPairingStore(),
	}
}

func TestRegisterAndGet(t *testing.T) {
	h := newTestHub()
	send := make(chan []byte, 1)
	h.Register("dev-1", send, "127.0.0.1:5000")

	c := h.Get("dev-1")
	if c == nil {
		t.Fatal("expected connection for dev-1")
	}
	if c.DeviceID != "dev-1" {
		t.Fatalf("expected device_id=dev-1, got %s", c.DeviceID)
	}
	if c.ConnectedIP != "127.0.0.1:5000" {
		t.Fatalf("expected connected_ip=127.0.0.1:5000, got %s", c.ConnectedIP)
	}
}

func TestRegisterReplacesOld(t *testing.T) {
	h := newTestHub()
	old := make(chan []byte, 1)
	h.Register("dev-1", old, "")

	newCh := make(chan []byte, 1)
	h.Register("dev-1", newCh, "")

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
	h := newTestHub()
	send := make(chan []byte, 1)
	h.Register("dev-1", send, "")
	h.Unregister("dev-1")

	if h.Get("dev-1") != nil {
		t.Fatal("expected nil after unregister")
	}

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
	h := newTestHub()
	send := make(chan []byte, 16)
	h.Register("dev-1", send, "")

	ch := make(chan []byte, 1)
	h.pendingMu.Lock()
	h.pending["dev-1|42"] = &pendingReq{
		ch:    ch,
		timer: time.AfterFunc(time.Hour, func() {}),
	}
	h.pendingMu.Unlock()

	h.Unregister("dev-1")

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
	h := newTestHub()
	h.Register("a", make(chan []byte, 1), "")
	h.Register("b", make(chan []byte, 1), "")
	h.Register("c", make(chan []byte, 1), "")

	ids := h.ListDeviceIDs()
	if len(ids) != 3 {
		t.Fatalf("expected 3 devices, got %d", len(ids))
	}
}

func TestListDeviceInfos(t *testing.T) {
	h := newTestHub()

	// Register a device credential with metadata
	h.creds.RegisterDevice("dev-1", "MacBook-Pro", "192.168.1.10", "AA:BB:CC:DD:EE:FF")

	// Connect dev-1
	h.Register("dev-1", make(chan []byte, 1), "10.0.0.5:9999")

	// Register another credential but don't connect
	h.creds.RegisterDevice("dev-2", "Ubuntu-Server", "192.168.1.20", "11:22:33:44:55:66")

	infos := h.ListDeviceInfos()
	if len(infos) != 2 {
		t.Fatalf("expected 2 device infos, got %d", len(infos))
	}

	infoMap := make(map[string]DeviceInfo)
	for _, i := range infos {
		infoMap[i.DeviceID] = i
	}

	d1 := infoMap["dev-1"]
	if d1.DeviceName != "MacBook-Pro" {
		t.Fatalf("expected MacBook-Pro, got %s", d1.DeviceName)
	}
	if !d1.Connected {
		t.Fatal("dev-1 should be connected")
	}
	if d1.ConnectedIP != "10.0.0.5:9999" {
		t.Fatalf("expected connected_ip=10.0.0.5:9999, got %s", d1.ConnectedIP)
	}

	d2 := infoMap["dev-2"]
	if d2.DeviceName != "Ubuntu-Server" {
		t.Fatalf("expected Ubuntu-Server, got %s", d2.DeviceName)
	}
	if d2.Connected {
		t.Fatal("dev-2 should not be connected")
	}
}

func TestForwardAndDeliver(t *testing.T) {
	h := newTestHub()
	send := make(chan []byte, 16)
	h.Register("dev-1", send, "")

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
	h := newTestHub()
	req := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`
	_, ok := h.ForwardRequest("nonexistent", []byte(req), time.Second)
	if ok {
		t.Fatal("expected ForwardRequest to fail for missing device")
	}
}

func TestForwardRequestTimeout(t *testing.T) {
	h := newTestHub()
	send := make(chan []byte, 16)
	h.Register("dev-1", send, "")

	req := `{"jsonrpc":"2.0","id":99,"method":"slow","params":{}}`
	_, ok := h.ForwardRequest("dev-1", []byte(req), 100*time.Millisecond)
	if ok {
		t.Fatal("expected timeout")
	}
}

func TestForwardRequestDuplicateID(t *testing.T) {
	h := newTestHub()
	send := make(chan []byte, 16)
	h.Register("dev-1", send, "")

	req := `{"jsonrpc":"2.0","id":1,"method":"test","params":{}}`

	go func() {
		h.ForwardRequest("dev-1", []byte(req), 5*time.Second)
	}()
	<-send

	_, ok := h.ForwardRequest("dev-1", []byte(req), time.Second)
	if ok {
		t.Fatal("expected duplicate request ID to fail")
	}
}
