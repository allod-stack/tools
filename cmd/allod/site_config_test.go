//go:build site

package main

// Tests for 'allod site config'. The command's two seams are stubbed, so no
// rclone runs and no terminal is opened, and the stub keeps every argv it was
// handed — which is what makes the central claim checkable: the password
// reaches rclone on stdin and appears nowhere else.
//
// The seams are package-level mutable state, so no test here calls t.Parallel.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Harness ---

const (
	testHost     = "ftp.example.com"
	testUser     = "example-user"
	testPassword = "correct horse battery staple"
	testObscured = "hT5xNSJ0bWZ4bkNZbEZmT0hCcWJn"
)

// runCall is one invocation of the exec seam, kept whole so a test can look at
// the command line and at what went to stdin separately.
type runCall struct {
	name  string
	args  []string
	input string
}

type configStub struct {
	// configFile is what 'rclone config file' reports. Empty means rclone
	// could not answer, which is what happens when there is no config yet.
	configFile    string
	obscured      string
	obscureStatus int
	host          string
	user          string
	password      string
	askErr        error
	askCalls      int
	runs          []runCall
}

func useConfigStub(t *testing.T, stub *configStub) {
	t.Helper()
	if stub.obscured == "" && stub.obscureStatus == 0 {
		stub.obscured = testObscured
	}
	previousRun, previousAsk := siteRun, siteAsk
	siteRun = func(input, name string, args ...string) (string, int) {
		stub.runs = append(stub.runs, runCall{name: name, args: append([]string(nil), args...), input: input})
		switch strings.Join(args, " ") {
		case "config file":
			if stub.configFile == "" {
				return "", 1
			}
			// rclone prints a sentence and then the path.
			return "Configuration file is stored at:\n" + stub.configFile, 0
		case "obscure -":
			return stub.obscured, stub.obscureStatus
		}
		t.Errorf("unexpected command: %s %v", name, args)
		return "", 1
	}
	siteAsk = func() (string, string, string, error) {
		stub.askCalls++
		return stub.host, stub.user, stub.password, stub.askErr
	}
	t.Cleanup(func() { siteRun, siteAsk = previousRun, previousAsk })
	stubTools(t, "rclone")
}

// answeringStub is the ordinary case: rclone reports a config path inside a
// directory that does not exist yet, and the operator answers all three
// prompts.
func answeringStub(t *testing.T) *configStub {
	t.Helper()
	return &configStub{
		configFile: filepath.Join(t.TempDir(), "config", "rclone", "rclone.conf"),
		host:       testHost,
		user:       testUser,
		password:   testPassword,
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read %s: %v", path, err)
	}
	return string(data)
}

// --- Writing the remote ---

func TestSiteConfigWritesTheStanza(t *testing.T) {
	stub := answeringStub(t)
	useConfigStub(t, stub)

	out, errText, code := runAllod(t, "site", "config")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, errText)
	}
	if errText != "" {
		t.Errorf("stderr = %q, want empty", errText)
	}

	want := "[shared]\ntype = ftp\nhost = " + testHost + "\nuser = " + testUser +
		"\npass = " + testObscured + "\nexplicit_tls = true\n"
	if got := readFile(t, stub.configFile); got != want {
		t.Errorf("config file =\n%q\nwant\n%q", got, want)
	}

	// The file holds a credential rclone can turn back into a password, so
	// its mode is part of the contract, not a detail of how it was written.
	info, err := os.Stat(stub.configFile)
	if err != nil {
		t.Fatalf("could not stat %s: %v", stub.configFile, err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("config file mode = %04o, want 0600", perm)
	}

	for _, want := range []string{"Remote: shared\n", "Type: ftp\n", "Host: " + testHost + "\n",
		"User: " + testUser + "\n", "Config: " + stub.configFile + "\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout does not contain %q\ngot: %q", want, out)
		}
	}
}

// TestSiteConfigKeepsThePasswordOffTheCommandLine is the reason the command
// exists in this shape. An argument is readable by every account on the machine
// for as long as the process lives, and rclone's obscuring is reversible, so
// neither the password nor its obscured form may appear in an argv or on a
// stream.
func TestSiteConfigKeepsThePasswordOffTheCommandLine(t *testing.T) {
	stub := answeringStub(t)
	useConfigStub(t, stub)

	out, errText, code := runAllod(t, "site", "config")

	// Checked before the exit status, deliberately: a command line that
	// carried the password is a leak whether or not the command succeeded.
	var obscureCalls int
	for _, call := range stub.runs {
		for _, arg := range append([]string{call.name}, call.args...) {
			if strings.Contains(arg, testPassword) {
				t.Errorf("the password appears in an argv: %s %v", call.name, call.args)
			}
			if strings.Contains(arg, testObscured) {
				t.Errorf("the obscured password appears in an argv: %s %v", call.name, call.args)
			}
		}
		if call.name == "rclone" && strings.Join(call.args, " ") == "obscure -" {
			obscureCalls++
			// No trailing newline: rclone reads the whole of a non-terminal
			// stdin, and one it did not strip would become part of the stored
			// password.
			if call.input != testPassword {
				t.Errorf("stdin to 'rclone obscure -' = %q, want the password exactly", call.input)
			}
		}
	}
	if obscureCalls != 1 {
		t.Errorf("'rclone obscure -' ran %d times, want 1", obscureCalls)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0\nstderr: %s", code, errText)
	}

	for name, text := range map[string]string{"stdout": out, "stderr": errText} {
		if strings.Contains(text, testPassword) {
			t.Errorf("%s contains the password: %q", name, text)
		}
		if strings.Contains(text, testObscured) {
			t.Errorf("%s contains the obscured password: %q", name, text)
		}
	}
}

// --- Not clobbering what is already there ---

func TestSiteConfigRefusesAnExistingRemote(t *testing.T) {
	stub := answeringStub(t)
	useConfigStub(t, stub)
	existing := "[shared]\ntype = ftp\nhost = old.example\nuser = bob\npass = OLD\nexplicit_tls = true\n"
	writeExistingConfig(t, stub.configFile, existing)

	_, errText, code := runAllod(t, "site", "config")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if got := readFile(t, stub.configFile); got != existing {
		t.Errorf("config file was modified:\n%q", got)
	}
	// Refused before the prompts: nobody types a password to be told it was
	// never going to be stored.
	if stub.askCalls != 0 {
		t.Errorf("the operator was asked %d times before the refusal, want 0", stub.askCalls)
	}
	for _, want := range []string{"already exists", "--force"} {
		if !strings.Contains(errText, want) {
			t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
		}
	}
}

// --force replaces the one stanza and leaves the rest of the file alone. The
// rclone configuration is machine-wide: the other remotes in it belong to
// whoever put them there.
func TestSiteConfigForceReplacesOnlyTheSharedStanza(t *testing.T) {
	stub := answeringStub(t)
	useConfigStub(t, stub)
	writeExistingConfig(t, stub.configFile,
		"[backup]\ntype = s3\nprovider = Other\n\n"+
			"[shared]\ntype = ftp\nhost = old.example\nuser = bob\npass = OLD\nexplicit_tls = true\n\n"+
			"[scratch]\ntype = local\n")

	_, errText, code := runAllod(t, "site", "config", "--force")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, errText)
	}

	want := "[backup]\ntype = s3\nprovider = Other\n\n" +
		"[shared]\ntype = ftp\nhost = " + testHost + "\nuser = " + testUser +
		"\npass = " + testObscured + "\nexplicit_tls = true\n\n" +
		"[scratch]\ntype = local\n"
	if got := readFile(t, stub.configFile); got != want {
		t.Errorf("config file =\n%q\nwant\n%q", got, want)
	}
	info, err := os.Stat(stub.configFile)
	if err != nil {
		t.Fatalf("could not stat %s: %v", stub.configFile, err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("config file mode = %04o, want 0600", perm)
	}
}

// A configuration with other remotes but no 'shared' is appended to, not
// refused and not replaced.
func TestSiteConfigAppendsToAnExistingFile(t *testing.T) {
	stub := answeringStub(t)
	useConfigStub(t, stub)
	writeExistingConfig(t, stub.configFile, "[backup]\ntype = s3\n")

	if _, errText, code := runAllod(t, "site", "config"); code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, errText)
	}
	want := "[backup]\ntype = s3\n\n[shared]\ntype = ftp\nhost = " + testHost +
		"\nuser = " + testUser + "\npass = " + testObscured + "\nexplicit_tls = true\n"
	if got := readFile(t, stub.configFile); got != want {
		t.Errorf("config file =\n%q\nwant\n%q", got, want)
	}
}

func writeExistingConfig(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("could not create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatalf("could not write %s: %v", path, err)
	}
}

// --- Where the file lives ---

// When rclone cannot say where its configuration is — an old rclone, or one
// that fails because there is none yet — the XDG default is used, which is
// where rclone itself would create one.
func TestSiteConfigFallsBackToTheXDGPath(t *testing.T) {
	tests := []struct {
		name string
		// setEnv returns the config path the command should choose.
		setEnv func(t *testing.T) string
	}{
		{"XDG_CONFIG_HOME", func(t *testing.T) string {
			base := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", base)
			return filepath.Join(base, "rclone", "rclone.conf")
		}},
		{"HOME", func(t *testing.T) string {
			home := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", "")
			t.Setenv("HOME", home)
			return filepath.Join(home, ".config", "rclone", "rclone.conf")
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want := test.setEnv(t)
			stub := &configStub{host: testHost, user: testUser, password: testPassword}
			useConfigStub(t, stub)

			out, errText, code := runAllod(t, "site", "config")
			if code != 0 {
				t.Fatalf("exit code = %d, want 0\nstderr: %s", code, errText)
			}
			if !strings.Contains(out, "Config: "+want+"\n") {
				t.Errorf("stdout does not report %q\ngot: %q", want, out)
			}
			if !strings.Contains(readFile(t, want), "[shared]\n") {
				t.Errorf("%s does not hold the remote", want)
			}
		})
	}
}

func TestLastNonBlankLine(t *testing.T) {
	tests := map[string]string{
		"Configuration file is stored at:\n/home/a/.config/rclone/rclone.conf": "/home/a/.config/rclone/rclone.conf",
		"/home/a/.config/rclone/rclone.conf\n\n":                               "/home/a/.config/rclone/rclone.conf",
		"/one/line":                                                            "/one/line",
		"":                                                                     "",
		"\n \n":                                                                "",
	}
	for text, want := range tests {
		if got := lastNonBlankLine(text); got != want {
			t.Errorf("lastNonBlankLine(%q) = %q, want %q", text, got, want)
		}
	}
}

// --- Refusals ---

func TestSiteConfigRejectsUnexpectedArguments(t *testing.T) {
	tests := []struct {
		args   []string
		errHas string
	}{
		{[]string{"site", "config", "--remote", "other"}, "unknown option for site config"},
		{[]string{"site", "config", "shared"}, "unexpected argument for site config"},
	}
	for _, test := range tests {
		t.Run(strings.Join(test.args[2:], " "), func(t *testing.T) {
			stub := answeringStub(t)
			useConfigStub(t, stub)

			_, errText, code := runAllod(t, test.args...)
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if !strings.Contains(errText, test.errHas) {
				t.Errorf("stderr does not contain %q\ngot: %q", test.errHas, errText)
			}
			if stub.askCalls != 0 {
				t.Errorf("the operator was asked %d times, want 0", stub.askCalls)
			}
		})
	}
}

func TestSiteConfigNeedsRclone(t *testing.T) {
	stub := answeringStub(t)
	useConfigStub(t, stub)
	t.Setenv("PATH", t.TempDir())

	_, errText, code := runAllod(t, "site", "config")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := "'rclone' not found on PATH"; !strings.Contains(errText, want) {
		t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
	}
}

// An answer that could not be one config value is refused rather than escaped:
// a newline would end the line and turn the rest of the answer into further
// rclone directives, in a file that may hold other people's remotes.
func TestSiteConfigRejectsUnusableAnswers(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		user     string
		password string
		errHas   string
	}{
		{"newline in the host", "a.example\npass = injected", testUser, testPassword, "host must be one line"},
		{"empty host", "", testUser, testPassword, "host must be one line"},
		{"newline in the user", testHost, "bob\n[other]", testPassword, "user must be one line"},
		{"empty user", testHost, "", testPassword, "user must be one line"},
		{"empty password", testHost, testUser, "", "password must not be empty"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := answeringStub(t)
			stub.host, stub.user, stub.password = test.host, test.user, test.password
			useConfigStub(t, stub)

			_, errText, code := runAllod(t, "site", "config")
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if !strings.Contains(errText, test.errHas) {
				t.Errorf("stderr does not contain %q\ngot: %q", test.errHas, errText)
			}
			if _, err := os.Stat(stub.configFile); err == nil {
				t.Errorf("%s was written despite the refusal", stub.configFile)
			}
		})
	}
}

func TestSiteConfigObscureFailure(t *testing.T) {
	stub := answeringStub(t)
	stub.obscured, stub.obscureStatus = "", 1
	useConfigStub(t, stub)

	_, errText, code := runAllod(t, "site", "config")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if want := "could not obscure the password"; !strings.Contains(errText, want) {
		t.Errorf("stderr does not contain %q\ngot: %q", want, errText)
	}
	if _, err := os.Stat(stub.configFile); err == nil {
		t.Errorf("%s was written after the obscure step failed", stub.configFile)
	}
	if strings.Contains(errText, testPassword) {
		t.Errorf("stderr contains the password: %q", errText)
	}
}

// --- The pieces ---

func TestValidConfigValue(t *testing.T) {
	valid := []string{"ftp.example.com", "user@example", "a space is fine", "ünïcode"}
	invalid := []string{"", "two\nlines", "a\ttab", "a\rreturn", "nul\x00byte", "bell\x07"}
	for _, value := range valid {
		if !validConfigValue(value) {
			t.Errorf("validConfigValue(%q) = false, want true", value)
		}
	}
	for _, value := range invalid {
		if validConfigValue(value) {
			t.Errorf("validConfigValue(%q) = true, want false", value)
		}
	}
}

func TestHasStanza(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"", false},
		{"[shared]\n", true},
		{"[backup]\ntype = s3\n", false},
		{"[backup]\n\n  [shared]  \ntype = ftp\n", true},
		{"[shared-staging]\n", false},
		{"# [shared]\n", false},
	}
	for _, test := range tests {
		if got := hasStanza(test.text, siteRemoteName); got != test.want {
			t.Errorf("hasStanza(%q) = %v, want %v", test.text, got, test.want)
		}
	}
}

func TestReplaceStanza(t *testing.T) {
	stanza := "[shared]\nnew = yes\n"
	tests := []struct {
		name string
		text string
		want string
	}{
		{"empty file", "", stanza},
		{"whitespace only", "\n\n", stanza},
		{"no such stanza", "[backup]\ntype = s3\n", "[backup]\ntype = s3\n\n" + stanza},
		{"no trailing newline", "[backup]\ntype = s3", "[backup]\ntype = s3\n\n" + stanza},
		{"only that stanza", "[shared]\nold = yes\n", stanza},
		{"stanza last", "[backup]\ntype = s3\n\n[shared]\nold = yes\n", "[backup]\ntype = s3\n\n" + stanza},
		{"stanza first", "[shared]\nold = yes\n\n[backup]\ntype = s3\n", stanza + "\n[backup]\ntype = s3\n"},
		{
			"stanza in the middle",
			"[backup]\ntype = s3\n\n[shared]\nold = yes\n\n[scratch]\ntype = local\n",
			"[backup]\ntype = s3\n\n" + stanza + "\n[scratch]\ntype = local\n",
		},
		{"comments outside it survive", "# mine\n[backup]\ntype = s3\n", "# mine\n[backup]\ntype = s3\n\n" + stanza},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := replaceStanza(test.text, siteRemoteName, stanza); got != test.want {
				t.Errorf("replaceStanza(%q) =\n%q\nwant\n%q", test.text, got, test.want)
			}
		})
	}
}

func TestRemoteStanza(t *testing.T) {
	// Pinned in full: rclone reads exactly these keys, and 'explicit_tls' is
	// what keeps the credential off the wire in the clear.
	want := "[shared]\ntype = ftp\nhost = h\nuser = u\npass = p\nexplicit_tls = true\n"
	if got := remoteStanza("h", "u", "p"); got != want {
		t.Errorf("remoteStanza =\n%q\nwant\n%q", got, want)
	}
}

// writeRcloneConfig is the only writer, so it is the only place the mode of a
// file holding a reversible credential can be decided. A config left wider by
// something else is narrowed, not left as it was found.
func TestWriteRcloneConfigNarrowsAWideFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "rclone.conf")
	writeExistingConfig(t, path, "[backup]\n")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatalf("could not widen %s: %v", path, err)
	}
	if err := writeRcloneConfig(path, "[shared]\n"); err != nil {
		t.Fatalf("writeRcloneConfig: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("could not stat %s: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("config file mode = %04o, want 0600", perm)
	}
	if got := readFile(t, path); got != "[shared]\n" {
		t.Errorf("config file = %q", got)
	}
	// The temporary file it wrote through is not left behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("could not read %s: %v", filepath.Dir(path), err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want 1", len(entries))
	}
}
