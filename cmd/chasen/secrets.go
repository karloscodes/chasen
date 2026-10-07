package main

import (
	"cmp"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// The secrets of an app, the way Rails keeps its credentials: one encrypted
// file in the repository, and one key outside it.
//
//	chasen.secrets.enc   the secrets, encrypted. It goes into git
//	chasen.key           the key. Git ignores it. In CI it is CHASEN_KEY
//
// `chasen secrets edit` opens the secrets in the editor and encrypts them
// again when the editor closes. Decrypted, the file is KEY=VALUE lines, and
// every one of them is a secret of the app: chasen deploy sends them all.
const (
	secretsFile = "chasen.secrets.enc"
	keyFile     = "chasen.key"
	keyEnv      = "CHASEN_KEY"
	// The first characters of the file: the format, so it can change one day.
	secretsFormat = "v1:"
)

const secretsUsage = `usage:
  chasen secrets         List the names of the secrets
  chasen secrets edit    Change the secrets in your editor. Makes the file and its key the first time
  chasen secrets show    Print the secrets, with their values`

func secretsCommand(args []string) error {
	switch {
	case len(args) == 0:
		return listSecrets()
	case len(args) == 1 && args[0] == "edit":
		return editSecrets()
	case len(args) == 1 && args[0] == "show":
		text, err := readSecrets()
		if err != nil {
			return err
		}
		fmt.Print(text)
		return nil
	}
	return errors.New(secretsUsage)
}

func listSecrets() error {
	text, err := readSecrets()
	if err != nil {
		return err
	}
	names := secretNames(text)
	if len(names) == 0 {
		fmt.Println("No secrets yet. Run: chasen secrets edit")
	}
	for _, name := range names {
		fmt.Println(name)
	}
	return nil
}

// secretNames returns the names in the text of the secrets, in its order.
func secretNames(text string) []string {
	var names []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimPrefix(strings.TrimSpace(line), "export ")
		if name, _, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
			names = append(names, strings.TrimSpace(name))
		}
	}
	return names
}

// storedSecrets returns the secrets of the file of this directory, or none
// when the directory has no file.
func storedSecrets() (map[string]string, error) {
	text, err := readSecrets()
	if err != nil {
		return nil, err
	}
	return parseDotenv(text), nil
}

// readSecrets returns the decrypted text of the secrets, or "" when this
// directory has no file.
func readSecrets() (string, error) {
	data, err := os.ReadFile(secretsFile)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	key, err := findKey()
	if err != nil {
		return "", err
	}
	if key == nil {
		return "", fmt.Errorf("%s is here, and its key is not. Put the key in %s, or in the environment as %s", secretsFile, keyFile, keyEnv)
	}
	return decrypt(key, string(data))
}

func writeSecrets(key []byte, text string) error {
	sealed, err := encrypt(key, text)
	if err != nil {
		return err
	}
	return os.WriteFile(secretsFile, []byte(sealed), 0644)
}

// findKey returns the key: CHASEN_KEY, then chasen.key in this directory,
// then in each directory above it. So one key at the top of a repository
// opens the secrets of every app in it. It returns nil when there is none.
func findKey() ([]byte, error) {
	if value := os.Getenv(keyEnv); value != "" {
		return parseKey(value, keyEnv)
	}
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	for {
		path := filepath.Join(dir, keyFile)
		data, err := os.ReadFile(path)
		if err == nil {
			// A key in a directory that others can write, like /tmp, can be
			// anybody's: take only a key file of this user.
			if info, err := os.Stat(path); err == nil {
				if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
					return nil, fmt.Errorf("%s belongs to another user. Use a key of your own, or set %s", path, keyEnv)
				}
			}
			return parseKey(string(data), path)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if filepath.Dir(dir) == dir {
			return nil, nil
		}
		dir = filepath.Dir(dir)
	}
}

func parseKey(value, from string) ([]byte, error) {
	key, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%s is not a key of chasen: a key is 64 characters, 0-9 and a-f", from)
	}
	return key, nil
}

// encrypt seals the text with AES-256-GCM. The result is one line of text.
func encrypt(key []byte, text string) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return secretsFormat + base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(text), nil)) + "\n", nil
}

func decrypt(key []byte, sealed string) (string, error) {
	encoded, ok := strings.CutPrefix(strings.TrimSpace(sealed), secretsFormat)
	if !ok {
		return "", fmt.Errorf("%s has a format that this chasen does not know. Is there a newer chasen? Run: chasen update", secretsFile)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	gcm, gcmErr := newGCM(key)
	if gcmErr != nil {
		return "", gcmErr
	}
	if err != nil || len(data) < gcm.NonceSize() {
		return "", fmt.Errorf("%s is damaged. Get it back from git: git checkout %s", secretsFile, secretsFile)
	}
	text, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("the key does not open %s. It is another key, or the file changed outside chasen", secretsFile)
	}
	return string(text), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// editSecrets opens the secrets in the editor of the user and encrypts what
// the editor leaves. The first time, it makes the key.
func editSecrets() error {
	editor := cmp.Or(os.Getenv("VISUAL"), os.Getenv("EDITOR"))
	if editor == "" {
		return errors.New(`no $VISUAL or $EDITOR to open the secrets in. Give one like this: EDITOR="code --wait" chasen secrets edit`)
	}
	app, err := loadAppFile()
	if err != nil {
		return err
	}
	text, err := readSecrets()
	if err != nil {
		return err
	}
	key, err := findKey()
	if err != nil {
		return err
	}
	if key == nil {
		if key, err = makeKey(); err != nil {
			return err
		}
	}
	before := text
	if _, err := os.Stat(secretsFile); errors.Is(err, fs.ErrNotExist) {
		text = fmt.Sprintf(`# The secrets of %s, one on each line: NAME=value
# chasen deploy sends them to the server, and the app reads them from its environment.
# A value with spaces or line breaks goes in double quotes: KEY="first line\nsecond line"
`, app.Name)
		before = "" // a new file is saved, also when the editor changes nothing
	}

	// The decrypted text is in a file only while the editor is open, in a
	// directory that only this user reads.
	dir, err := os.MkdirTemp("", "chasen-secrets-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, app.Name+".env")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		return err
	}
	// The editor can be a command with options, like "code --new-window".
	run := exec.Command("sh", "-c", editorCommand(editor)+` "$1"`, "sh", path)
	run.Stdin, run.Stdout, run.Stderr = os.Stdin, os.Stdout, os.Stderr
	opened := time.Now()
	if err := run.Run(); err != nil {
		return fmt.Errorf("the editor failed (%w). Nothing changed", err)
	}
	edited, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(edited) == before {
		fmt.Println("Nothing changed.")
		// An editor that chasen does not know came back before a person could type.
		if time.Since(opened) < time.Second {
			fmt.Println(`Did the editor open a window and come back at once? Then it needs its option to wait: EDITOR="youreditor --wait" chasen secrets edit`)
		}
		return nil
	}
	for i, line := range strings.Split(string(edited), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") && !strings.Contains(line, "=") {
			fmt.Fprintf(os.Stderr, "Warning: line %d is not NAME=value. chasen does not send it.\n", i+1)
		}
	}
	if err := writeSecrets(key, string(edited)); err != nil {
		return err
	}
	fmt.Printf("Encrypted and saved %s. Commit it.\n", secretsFile)
	return nil
}

// The editors with a window that come back at once: the command returns
// while the window is still open. Each one waits for its window with --wait.
var windowEditors = []string{"code", "code-insiders", "codium", "cursor", "windsurf", "zed", "subl", "atom", "mate"}

// editorCommand returns the command that opens the editor and waits until
// the person closes the file. An editor with a window, like VS Code, gets
// --wait: without it, chasen would read the file before anybody typed.
func editorCommand(editor string) string {
	words := strings.Fields(editor)
	if len(words) == 0 || !slices.Contains(windowEditors, filepath.Base(words[0])) {
		return editor
	}
	if slices.Contains(words[1:], "--wait") || slices.Contains(words[1:], "-w") {
		return editor
	}
	return editor + " --wait"
}

// makeKey makes the key of a new secrets file, in this directory, and tells
// git to leave it out.
func makeKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyFile, []byte(hex.EncodeToString(key)+"\n"), 0600); err != nil {
		return nil, err
	}
	ignored := "Keep it out of git."
	// In a git repository: ignore the key, unless a rule ignores it already.
	if exec.Command("git", "rev-parse", "--is-inside-work-tree").Run() == nil {
		if exec.Command("git", "check-ignore", "-q", keyFile).Run() != nil {
			rules, _ := os.ReadFile(".gitignore")
			if len(rules) > 0 && !strings.HasSuffix(string(rules), "\n") {
				rules = append(rules, '\n')
			}
			if err := os.WriteFile(".gitignore", append(rules, []byte(keyFile+"\n")...), 0644); err != nil {
				return nil, err
			}
		}
		ignored = "Git ignores it."
	}
	fmt.Printf(`Made %s, the key that opens %s:

  %s

Save it in a password manager. If you lose the key, nobody can read the secrets, and that includes you.
%s In CI, give the key as %s.

`, keyFile, secretsFile, hex.EncodeToString(key), ignored, keyEnv)
	return key, nil
}

// keepSecretKey makes the secret key of a new app and saves it with the
// secrets of the directory. Then the key is in the repository, encrypted,
// and a new server gives the app the same one. It does nothing for a
// directory with no secrets file, for secrets that have the key already, and
// for an app that runs: that app has a key, which the server made, and a new
// one would log everybody out.
func keepSecretKey(deployed func() bool) error {
	if _, err := os.Stat(secretsFile); err != nil {
		return nil
	}
	text, err := readSecrets()
	if err != nil {
		return err
	}
	if stored := parseDotenv(text); stored["SECRET_KEY_BASE"] != "" || stored["PRIVATE_KEY"] != "" || deployed() {
		return nil
	}
	key, err := findKey()
	if err != nil {
		return err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += "\n# The key that the app signs its sessions with. Chasen made it at the first deploy. Keep it.\nSECRET_KEY_BASE=" + hex.EncodeToString(secret) + "\n"
	if err := writeSecrets(key, text); err != nil {
		return err
	}
	fmt.Printf("Made SECRET_KEY_BASE for the app and saved it in %s. Commit the file: a new server then gives the app the same key.\n", secretsFile)
	return nil
}
