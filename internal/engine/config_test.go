package engine

import (
	"testing"
)

func TestGeneratePrivateKey(t *testing.T) {
	t.Run("generates 64 char hex string", func(t *testing.T) {
		key, err := GeneratePrivateKey()
		if err != nil {
			t.Fatalf("GeneratePrivateKey() error = %v", err)
		}
		if len(key) != 64 {
			t.Errorf("GeneratePrivateKey() length = %d, want 64", len(key))
		}
	})

	t.Run("generates unique keys", func(t *testing.T) {
		keys := make(map[string]bool)
		for i := 0; i < 100; i++ {
			key, _ := GeneratePrivateKey()
			if keys[key] {
				t.Errorf("GeneratePrivateKey() produced duplicate key")
			}
			keys[key] = true
		}
	})
}
