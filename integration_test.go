//go:build integration

package gradual

import (
	"context"
	"testing"
	"time"
)

func TestIntegration_GetFlag(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client := NewClient(ctx, Options{
		APIKey:      "grdl_x5Hf17fFpgjkUykTZa7bGerkZsCd-OJX",
		Environment: "testing",
	})
	defer client.Close()

	if err := client.WaitUntilReady(ctx); err != nil {
		t.Fatalf("client failed to initialize: %v", err)
	}

	got := client.Get("testing", nil)
	if got != "variation-value-2" {
		t.Fatalf("expected %q, got %v", "variation-value-2", got)
	}
}
