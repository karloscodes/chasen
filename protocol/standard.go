package protocol

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The standard (STANDARD.md), the part that needs no Docker: what the
// settings of an app may say, and how the image and the settings together
// give the port, the health path, and the storage. The CLI checks with these
// rules before a deploy, and the server checks again when the deploy
// arrives: one rule, one place.

// The values for an image and a chasen.yml that say nothing.
const (
	DefaultPort          = 8080
	DefaultHealth        = "/up"
	DefaultHealthTimeout = 30
)

// DefaultMemory is the most memory an app may use when chasen.yml does not
// say. One server runs several apps, so one app must not take all of it.
const DefaultMemory = "512m"

var memoryRe = regexp.MustCompile(`^([1-9][0-9]{0,5})([mg])$`)

// ValidMemory reports a memory limit in the form of Docker: 512m, 2g. Less
// than 64 MB runs no real app.
func ValidMemory(memory string) bool {
	m := memoryRe.FindStringSubmatch(memory)
	if m == nil {
		return false
	}
	n, _ := strconv.Atoi(m[1])
	return m[2] == "g" || n >= 64
}

// DefaultVolumes is the default storage. Rails keeps its files in
// /rails/storage, so the same directory is at both paths.
var DefaultVolumes = []string{"/storage", "/rails/storage"}

// StandardEnv are the names that Chasen sets in every container. The
// settings of an app cannot set them.
var StandardEnv = []string{"PORT", "BASE_URL", "SECRET_KEY_BASE", "PRIVATE_KEY", "STORAGE_DIR", "DATABASE_PATH", "APP_VERSION", "APP_ENV"}

// The secret key of an app has two names: SECRET_KEY_BASE (Rails, ONCE) and
// PRIVATE_KEY (matcha). The server makes the key at the first deploy and
// keeps it. A deploy can bring the key as a secret instead: then the key is
// the owner's, and a new server gets the same one.
var secretKeyNames = []string{"SECRET_KEY_BASE", "PRIVATE_KEY"}

// SecretKey returns the secret key that the settings bring, or "".
func (s Settings) SecretKey() string {
	return cmp.Or(s.Env["SECRET_KEY_BASE"], s.Env["PRIVATE_KEY"])
}

var (
	appNameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	envKeyRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	domainRe  = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	healthRe  = regexp.MustCompile(`^/[A-Za-z0-9._~/-]*$`)
	// An image with its tag or digest: ghcr.io/you/app:3f9a2c1.
	imageRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*(:[0-9]+)?(/[a-z0-9._/-]+)*[:@][A-Za-z0-9._:-]{1,128}$`)
)

// ValidDomain reports a host name that an app or a server can have.
func ValidDomain(d string) bool { return len(d) <= 253 && domainRe.MatchString(d) }

// AppDomains decides the domains of an app at a deploy. An app that runs
// keeps the domains it has. A new app gets the domain that the deploy
// brings, or <name>.<base domain of the server>. A server with no base
// domain cannot make up a name, so there the first deploy must bring one.
// note is a line for the user, or "".
func AppDomains(name, base, sent string, has []string) (domains []string, note string, err error) {
	switch {
	case len(has) > 0:
		if sent != "" && !slices.Contains(has, sent) {
			note = fmt.Sprintf("The app keeps its domains (%s). To add %s, run: chasen domains add %s", strings.Join(has, ", "), sent, sent)
		}
		return has, note, nil
	case sent != "":
		return []string{sent}, "", nil
	case base != "":
		return []string{name + "." + base}, "", nil
	}
	return nil, "", fmt.Errorf("this server has no base domain, so it cannot name a new app. Give the app its domain: chasen deploy --domain %s.example.com", name)
}

// CheckAppName refuses a name that cannot be an app: it is a part of the
// domain of the app and the name of its container.
func CheckAppName(name string) error {
	if !appNameRe.MatchString(name) {
		return fmt.Errorf("invalid app name %q: use lowercase letters, digits, and hyphens. Set `name:` in chasen.yml", name)
	}
	// "api" is the host of the API, and the others are names the server uses itself.
	if name == "api" || name == "proxy" || name == "matcha-proxy" || name == "chasen-server" {
		return fmt.Errorf("the app name %q is reserved", name)
	}
	return nil
}

// CheckVolumes refuses paths the engine cannot keep apart. The engine maps a
// volume to /var/matcha/<app>/<last part of the path>.
func CheckVolumes(volumes []string) error {
	seen := map[string]string{}
	for _, v := range volumes {
		if !path.IsAbs(v) || path.Clean(v) != v || v == "/" {
			return fmt.Errorf("invalid volume %q: use an absolute path like /app/storage", v)
		}
		base := path.Base(v)
		if base == "backups" || strings.HasPrefix(base, "pre-restore-") || strings.HasSuffix(base, "-litestream") {
			return fmt.Errorf("the volume name %q is reserved", base)
		}
		if other, ok := seen[base]; ok {
			return fmt.Errorf("the volumes %s and %s end in the same name. Rename one", other, v)
		}
		seen[base] = v
	}
	return nil
}

// Check returns everything in the settings that no server accepts, joined
// in one error, or nil.
func (s Settings) Check() error {
	var problems []error
	for _, key := range slices.Sorted(maps.Keys(s.Env)) {
		switch {
		case !envKeyRe.MatchString(key):
			problems = append(problems, fmt.Errorf("invalid env name %q: use letters, digits, and _", key))
		case slices.Contains(StandardEnv, key) && !slices.Contains(secretKeyNames, key):
			problems = append(problems, fmt.Errorf("chasen sets %s itself. Remove it from chasen.yml", key))
		}
	}
	base, private := s.Env["SECRET_KEY_BASE"], s.Env["PRIVATE_KEY"]
	switch key := s.SecretKey(); {
	case base != "" && private != "" && base != private:
		problems = append(problems, errors.New("SECRET_KEY_BASE and PRIVATE_KEY are one secret in Chasen, and they have two values. Keep one of them in `secrets:`"))
	case key != "" && len(key) < 32:
		problems = append(problems, errors.New("the secret key is too short: use 32 characters or more. Make one: openssl rand -hex 32"))
	}
	if s.Image != "" && !imageRe.MatchString(s.Image) {
		problems = append(problems, fmt.Errorf("invalid image %q: use the form ghcr.io/you/app:tag, in lowercase", s.Image))
	}
	if s.Domain != "" && !ValidDomain(s.Domain) {
		problems = append(problems, fmt.Errorf("invalid domain %q: use a name like shop.example.com, in lowercase, with no https://", s.Domain))
	}
	if s.Port < 0 || s.Port > 65535 {
		problems = append(problems, fmt.Errorf("invalid port %d: use 1 to 65535", s.Port))
	}
	if s.Health != "" && !healthRe.MatchString(s.Health) {
		problems = append(problems, fmt.Errorf("invalid health path %q: it starts with / and has no query", s.Health))
	}
	if s.HealthTimeout < 0 || s.HealthTimeout > 900 {
		problems = append(problems, fmt.Errorf("invalid health_timeout %d: use 1 to 900 seconds", s.HealthTimeout))
	}
	if err := CheckVolumes(s.Volumes); err != nil {
		problems = append(problems, err)
	}
	if s.Memory != "" && !ValidMemory(s.Memory) {
		problems = append(problems, fmt.Errorf("invalid memory %q: use megabytes or gigabytes, like 512m or 2g, and 64m or more", s.Memory))
	}
	return errors.Join(problems...)
}

// Shape is how an app meets the standard: the result of the image and the
// settings.
type Shape struct {
	Port          int
	PortFrom      string // where the port comes from, for messages
	Health        string
	HealthTimeout int
	Volumes       []string
}

// ShapeOf decides the port, the health path, and the volumes of an app: the
// settings first, then what the image declares with EXPOSE and VOLUME (its
// TCP ports and its volumes, sorted), then the defaults.
func ShapeOf(settings Settings, ports []int, volumes []string) (Shape, error) {
	s := Shape{
		Port: settings.Port, PortFrom: "chasen.yml",
		Health:        settings.Health,
		HealthTimeout: settings.HealthTimeout,
		Volumes:       settings.Volumes,
	}
	switch {
	case s.Port != 0:
	case len(ports) == 0:
		s.Port, s.PortFrom = DefaultPort, "the default, the image has no EXPOSE"
	case len(ports) == 1 || slices.Contains(ports, 80):
		s.Port = ports[0]
		if slices.Contains(ports, 80) {
			s.Port = 80
		}
		s.PortFrom = "EXPOSE in the image"
	default:
		return s, fmt.Errorf("the image declares the ports %v. Say which one serves HTTP: add `port:` to chasen.yml", ports)
	}
	if s.Health == "" {
		s.Health = DefaultHealth
	}
	if s.HealthTimeout == 0 {
		s.HealthTimeout = DefaultHealthTimeout
	}
	if len(s.Volumes) == 0 {
		// The image can declare volumes that the engine cannot keep apart. Then the override is needed.
		if s.Volumes = volumes; CheckVolumes(volumes) != nil {
			return s, fmt.Errorf("the image declares the volumes %v. %w. List them with `volumes:` in chasen.yml", volumes, CheckVolumes(volumes))
		}
	}
	if len(s.Volumes) == 0 {
		s.Volumes = DefaultVolumes
	}
	return s, nil
}
