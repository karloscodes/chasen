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

// The engine maps each mount to a folder of the host by its last name, so the
// asset path stays apart from the volumes.
func TestAssetPath(t *testing.T) {
	t.Run("in chasen.yml", func(t *testing.T) {
		cases := []struct {
			name    string
			assets  string
			volumes []string
			want    string // a part of the error, or "" for settings that are right
		}{
			{"the assets of Rails next to its storage", "/rails/public/assets", []string{"/rails/storage"}, ""},
			{"a relative path", "public/assets", nil, "absolute path"},
			{"the root of the image", "/", nil, "absolute path"},
			{"a reserved name", "/app/backups", nil, "reserved"},
			{"the last name of a volume", "/rails/public/storage", []string{"/rails/storage"}, "end in the same name"},
			{"inside a volume", "/rails/storage/assets", []string{"/rails/storage"}, "one inside the other"},
			{"around a volume", "/rails/public", []string{"/rails/public/uploads"}, "one inside the other"},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				err := Settings{AssetPath: c.assets, Volumes: c.volumes}.Check()

				if c.want == "" && err != nil {
					t.Errorf("Check() = %v, want no error", err)
				}
				if c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
					t.Errorf("Check() = %v, want an error with %q", err, c.want)
				}
			})
		}
	})

	t.Run("against the volumes of the image", func(t *testing.T) {
		_, err := ShapeOf(Settings{AssetPath: "/app/public/storage"}, nil, []string{"/app/storage"})

		if err == nil || !strings.Contains(err.Error(), "end in the same name") {
			t.Errorf("ShapeOf() = %v, want the clash with the VOLUME of the image", err)
		}
	})
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
