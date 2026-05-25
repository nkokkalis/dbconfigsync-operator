package dbclient

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestFetchConfig_UnsupportedType(t *testing.T) {
	_, err := FetchConfig(context.Background(), "sqlite", "file:test.db", "SELECT 1")
	if err == nil {
		t.Fatal("expected error for unsupported database type, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported database type") {
		t.Errorf("error message %q does not mention unsupported database type", err.Error())
	}
}

func TestFetchConfig_TypeAliasesAreRecognized(t *testing.T) {
	// These types must be dispatched (not rejected as unsupported), so any error
	// must come from the connection attempt, not the type switch.
	recognized := []string{"postgresql", "postgres", "mysql", "redis", "mongodb", "mongo"}

	for _, dbType := range recognized {
		t.Run(dbType, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, err := FetchConfig(ctx, dbType, "invalid-uri", "SELECT 1")
			if err != nil && strings.Contains(err.Error(), "unsupported database type") {
				t.Errorf("type %q was unexpectedly rejected: %v", dbType, err)
			}
		})
	}
}
