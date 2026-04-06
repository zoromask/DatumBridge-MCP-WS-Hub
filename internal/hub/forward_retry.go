package hub

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Forward send-buffer retries (when the device ws writer is briefly backlogged).
// Not applied to "device offline" or JSON-RPC timeouts — only to chan send contention.
func forwardSendMaxAttempts() int {
	const def = 4
	v := strings.TrimSpace(os.Getenv("HUB_FORWARD_SEND_RETRIES"))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return def
	}
	if n > 50 {
		return 50
	}
	return n
}

func forwardSendRetryInterval() time.Duration {
	const defMs = 25
	v := strings.TrimSpace(os.Getenv("HUB_FORWARD_SEND_RETRY_INTERVAL_MS"))
	if v == "" {
		return time.Duration(defMs) * time.Millisecond
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return time.Duration(defMs) * time.Millisecond
	}
	if n > 5000 {
		n = 5000
	}
	return time.Duration(n) * time.Millisecond
}
