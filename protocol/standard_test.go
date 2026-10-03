package protocol

import (
	"strings"
	"testing"
)

func TestSettingsCheck(t *testing.T) {
	long := strings.Repeat("k", 64)
	cases := []struct {
		name string
		env  map[string]string
		want string // a part of the error, or "" for settings that are right
	}{
		{"no secret key: the server makes one", map[string]string{"LOG_LEVEL": "info"}, ""},
		{"the secret key under the name of Rails", map[string]string{"SECRET_KEY_BASE": long}, ""},
		{"the secret key under the name of matcha", map[string]string{"PRIVATE_KEY": long}, ""},
		{"the same key under both names", map[string]string{"SECRET_KEY_BASE": long, "PRIVATE_KEY": long}, ""},
		{"two keys", map[string]string{"SECRET_KEY_BASE": long, "PRIVATE_KEY": long + "x"}, "one secret in Chasen"},
		{"a key that is too short", map[string]string{"SECRET_KEY_BASE": "short"}, "too short"},
		{"a name that Chasen sets", map[string]string{"PORT": "3000"}, "chasen sets PORT itself"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Settings{Env: c.env}.Check()

			if c.want == "" && err != nil {
				t.Errorf("Check() = %v, want no error", err)
			}
			if c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
				t.Errorf("Check() = %v, want an error with %q", err, c.want)
			}
		})
	}
}

func TestMemory(t *testing.T) {
	for memory, valid := range map[string]bool{
		"512m": true, "64m": true, "1g": true, "16g": true,
		"32m": false, "1.5g": false, "512": false, "512M": false, "0g": false, "1t": false,
	} {
		t.Run(memory, func(t *testing.T) {
			err := Settings{Memory: memory}.Check()

			if valid && err != nil || !valid && (err == nil || !strings.Contains(err.Error(), "invalid memory")) {
				t.Errorf("Check() with memory %q = %v, want valid %v", memory, err, valid)
			}
		})
	}
}
