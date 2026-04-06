package hub

import (
	"net/http"
	"testing"
)

func TestCorrelationIDFromRequest_Header(t *testing.T) {
	r := &http.Request{Header: http.Header{}}
	r.Header.Set("X-Request-ID", "req-abc-123")
	if got := CorrelationIDFromRequest(r); got != "req-abc-123" {
		t.Fatalf("got %q", got)
	}

	r2 := &http.Request{Header: http.Header{}}
	r2.Header.Set(HeaderCorrelationID, "primary")
	if got := CorrelationIDFromRequest(r2); got != "primary" {
		t.Fatalf("got %q", got)
	}
}

func TestCorrelationIDFromRequest_Traceparent(t *testing.T) {
	r := &http.Request{Header: http.Header{}}
	r.Header.Set("Traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	if got := CorrelationIDFromRequest(r); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("got %q", got)
	}
}

func TestWithCorrelationID_Context(t *testing.T) {
	r := &http.Request{Header: http.Header{}}
	r = WithCorrelationID(r, "fixed-id")
	if got := CorrelationIDFromContext(r); got != "fixed-id" {
		t.Fatalf("got %q", got)
	}
}
