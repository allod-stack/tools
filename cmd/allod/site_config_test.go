//go:build site

package main

// Tests for 'allod site config'. The command's effect seams are stubbed, so no
// rclone runs and no terminal is opened, and the stub keeps every argv it was
// handed — which is what makes the central claim checkable: the password
// reaches rclone on stdin and appears nowhere else.
//
// The seams are package-level mutable state, so no test here calls t.Parallel.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
	fieldCalls    []remoteConfigField
	runs          []runCall
}

func useConfigStub(t *testing.T, stub *configStub) {
	t.Helper()
	if stub.obscured == "" && stub.obscureStatus == 0 {
		stub.obscured = testObscured
	}
	previousRun, previousAsk, previousAskField := siteRun, siteAsk, siteAskField
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
	siteAskField = func(field remoteConfigField) (string, error) {
		stub.fieldCalls = append(stub.fieldCalls, field)
		switch field {
		case remoteConfigHost:
			return stub.host, stub.askErr
		case remoteConfigUser:
			return stub.user, stub.askErr
		case remoteConfigPassword:
			return stub.password, stub.askErr
		}
		return "", stub.askErr
	}
	t.Cleanup(func() {
		siteRun, siteAsk, siteAskField = previousRun, previousAsk, previousAskField
	})
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
		"User: " + testUser + "\n", "Config: " + stub.configFile + "\n",
		"Check it with: allod site check\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout does not contain %q\ngot: %q", want, out)
		}
	}
	if strings.Contains(out, "rclone lsd") {
		t.Errorf("stdout tells the operator to run an unavailable command: %q", out)
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

// 'replace' is the discoverable spelling of the legacy --force behavior: both
// ask for all three values and replace only the owned stanza.
func TestSiteConfigReplaceIsTheFullReplacementSpelling(t *testing.T) {
	stub := answeringStub(t)
	useConfigStub(t, stub)
	writeExistingConfig(t, stub.configFile,
		"[backup]\ntype = s3\n\n"+
			"[shared]\ntype = ftp\nhost = old.example\nuser = old-user\npass = OLD\nexplicit_tls = true\n")

	out, errText, code := runAllod(t, "site", "config", "replace")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, errText)
	}
	if stub.askCalls != 1 || len(stub.fieldCalls) != 0 {
		t.Errorf("full prompts = %d, field prompts = %v; want one full prompt", stub.askCalls, stub.fieldCalls)
	}
	want := "[backup]\ntype = s3\n\n" +
		"[shared]\ntype = ftp\nhost = " + testHost + "\nuser = " + testUser +
		"\npass = " + testObscured + "\nexplicit_tls = true\n"
	if got := readFile(t, stub.configFile); got != want {
		t.Errorf("config file =\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(out, "allod site check") {
		t.Errorf("replacement does not name the available check command: %q", out)
	}
}

func TestSiteConfigReplaceRejectsDuplicateRemoteBeforePrompting(t *testing.T) {
	commands := [][]string{
		{"site", "config", "replace"},
		{"site", "config", "--force"},
	}
	for _, command := range commands {
		t.Run(strings.Join(command[2:], " "), func(t *testing.T) {
			stub := answeringStub(t)
			useConfigStub(t, stub)
			existing := "[shared]\ntype = ftp\nhost = first.example\nuser = first\npass = FIRST-SECRET\nexplicit_tls = true\n" +
				"[shared]\ntype = ftp\nhost = second.example\nuser = second\npass = SECOND-SECRET\nexplicit_tls = true\n"
			writeExistingConfig(t, stub.configFile, existing)

			args := append(append([]string(nil), command...), "--config", stub.configFile)
			out, errText, code := runAllod(t, args...)
			if code != 1 || !strings.Contains(errText, "more than one 'shared' rclone remote") {
				t.Errorf("duplicate replacement: exit=%d stdout=%q stderr=%q", code, out, errText)
			}
			if got := readFile(t, stub.configFile); got != existing {
				t.Errorf("duplicate replacement modified the config: %q", got)
			}
			if stub.askCalls != 0 || len(stub.fieldCalls) != 0 {
				t.Errorf("duplicate replacement prompted: full=%d fields=%v", stub.askCalls, stub.fieldCalls)
			}
			if len(stub.runs) != 0 {
				t.Errorf("duplicate replacement ran rclone: %+v", stub.runs)
			}
			for _, secret := range []string{"FIRST-SECRET", "SECOND-SECRET"} {
				if strings.Contains(out+errText, secret) {
					t.Errorf("duplicate replacement printed a stored password: stdout=%q stderr=%q", out, errText)
				}
			}
		})
	}
}

// Each update asks one question and replaces one value byte range. The fixture
// deliberately uses CRLF, unusual spacing, comments, an unknown directive and
// unrelated stanzas so a normalising rewrite cannot pass.
func TestSiteConfigUpdateChangesExactlyTheSelectedField(t *testing.T) {
	const oldObscured = "OLD-OBSCURED-SECRET"
	existing := "# keep this byte for byte\r\n" +
		"[backup]\r\ntype = s3\r\nprovider = Other\r\n\r\n" +
		"[shared]\r\ntype: ftp\r\nhost   : old.example.test  \r\n" +
		"user = old-user\r\npass: " + oldObscured + "\r\n" +
		"explicit_tls = true\r\nunknown = untouched\r\n\r\n" +
		"[scratch]\r\ntype = local\r\n"

	tests := []struct {
		name        string
		field       remoteConfigField
		oldValue    string
		newValue    string
		wantObscure bool
	}{
		{"host", remoteConfigHost, "old.example.test", testHost, false},
		{"user", remoteConfigUser, "old-user", testUser, false},
		{"password", remoteConfigPassword, oldObscured, testObscured, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := answeringStub(t)
			useConfigStub(t, stub)
			writeExistingConfig(t, stub.configFile, existing)

			out, errText, code := runAllod(t, "site", "config", "update", string(test.field))
			if code != 0 {
				t.Fatalf("exit code = %d, want 0\nstderr: %s", code, errText)
			}
			if stub.askCalls != 0 {
				t.Errorf("all three fields were prompted %d times, want 0", stub.askCalls)
			}
			if len(stub.fieldCalls) != 1 || stub.fieldCalls[0] != test.field {
				t.Errorf("field prompts = %v, want exactly [%s]", stub.fieldCalls, test.field)
			}

			want := strings.Replace(existing, test.oldValue, test.newValue, 1)
			if got := readFile(t, stub.configFile); got != want {
				t.Errorf("config file =\n%q\nwant the selected value only:\n%q", got, want)
			}
			obscureCalls := 0
			for _, call := range stub.runs {
				for _, arg := range append([]string{call.name}, call.args...) {
					if strings.Contains(arg, testPassword) || strings.Contains(arg, testObscured) || strings.Contains(arg, oldObscured) {
						t.Errorf("a password appears in argv: %s %v", call.name, call.args)
					}
				}
				if call.name == "rclone" && strings.Join(call.args, " ") == "obscure -" {
					obscureCalls++
					if call.input != testPassword {
						t.Errorf("stdin to obscure = %q, want password exactly", call.input)
					}
				}
			}
			wantObscureCalls := 0
			if test.wantObscure {
				wantObscureCalls = 1
			}
			if obscureCalls != wantObscureCalls {
				t.Errorf("obscure calls = %d, want %d", obscureCalls, wantObscureCalls)
			}
			for stream, text := range map[string]string{"stdout": out, "stderr": errText} {
				for _, secret := range []string{testPassword, testObscured, oldObscured} {
					if strings.Contains(text, secret) {
						t.Errorf("%s contains a password: %q", stream, text)
					}
				}
			}
			if !strings.Contains(out, "Updated: "+string(test.field)+"\n") || !strings.Contains(out, "allod site check") {
				t.Errorf("stdout does not report the selected update and check command: %q", out)
			}
		})
	}
}

func TestSiteConfigShowPrintsOnlySafeFields(t *testing.T) {
	stub := answeringStub(t)
	useConfigStub(t, stub)
	writeExistingConfig(t, stub.configFile,
		"[shared]\ntype: ftp\nhost = "+testHost+"\nuser: "+testUser+
			"\npass = "+testObscured+"\nexplicit_tls: true\n")

	out, errText, code := runAllod(t, "site", "config", "show")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, errText)
	}
	want := "Config: " + stub.configFile + "\nRemote: shared\nType: ftp\nHost: " + testHost + "\nUser: " + testUser + "\n"
	if out != want {
		t.Errorf("stdout = %q, want safe inspection %q", out, want)
	}
	for stream, text := range map[string]string{"stdout": out, "stderr": errText} {
		for _, secret := range []string{testPassword, testObscured} {
			if strings.Contains(text, secret) {
				t.Errorf("%s contains the password or its obscured form: %q", stream, text)
			}
		}
	}
	if stub.askCalls != 0 || len(stub.fieldCalls) != 0 {
		t.Errorf("inspection prompted: full=%d fields=%v", stub.askCalls, stub.fieldCalls)
	}
}

func TestSiteConfigShowRejectsMissingOrDuplicateRequiredKeys(t *testing.T) {
	tests := []struct {
		name   string
		stanza string
		want   string
	}{
		{"missing host", "type = ftp\nuser = u\npass = SECRET\nexplicit_tls = true\n", "no host key"},
		{"duplicate user", "type = ftp\nhost = h\nuser = first\nuser: second\npass = SECRET\nexplicit_tls = true\n", "more than one user key"},
		{"missing password", "type = ftp\nhost = h\nuser = u\nexplicit_tls = true\n", "no pass key"},
		{"duplicate remote", "type = ftp\nhost = h\nuser = u\npass = SECRET\nexplicit_tls = true\n[shared]\ntype = ftp\nhost = h2\nuser = u2\npass = SECRET2\nexplicit_tls = true\n", "more than one 'shared'"},
		{"newline forges another host", "type = ftp\nhost = safe\nhost = FORGED\nuser = u\npass = SECRET\nexplicit_tls = true\n", "more than one host key"},
		{"control byte in host", "type = ftp\nhost = safe\x07FORGED\nuser = u\npass = SECRET\nexplicit_tls = true\n", "unusable host value"},
		{"terminal escape in user", "type = ftp\nhost = h\nuser = safe\x1b[2JFORGED\npass = SECRET\nexplicit_tls = true\n", "unusable user value"},
		{"wrong backend", "type = sftp\nhost = h\nuser = u\npass = SECRET\nexplicit_tls = true\n", "not an FTP remote"},
		{"TLS disabled", "type = ftp\nhost = h\nuser = u\npass = SECRET\nexplicit_tls = false\n", "does not require explicit TLS"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := answeringStub(t)
			useConfigStub(t, stub)
			writeExistingConfig(t, stub.configFile, "[shared]\n"+test.stanza)

			out, errText, code := runAllod(t, "site", "config", "show")
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if !strings.Contains(errText, test.want) {
				t.Errorf("stderr does not contain %q: %q", test.want, errText)
			}
			if out != "" || strings.Contains(errText, "SECRET") || strings.Contains(errText, "FORGED") || strings.Contains(errText, "\x1b") {
				t.Errorf("failed inspection exposed config content: stdout=%q stderr=%q", out, errText)
			}
			if stub.askCalls != 0 || len(stub.fieldCalls) != 0 {
				t.Errorf("failed inspection prompted: full=%d fields=%v", stub.askCalls, stub.fieldCalls)
			}
		})
	}
}

func TestSiteConfigUpdateRejectsAnAmbiguousFieldBeforePrompting(t *testing.T) {
	stub := answeringStub(t)
	useConfigStub(t, stub)
	existing := "[shared]\ntype = ftp\nhost = h\nuser = first\nuser = second\npass = SECRET\nexplicit_tls = true\n"
	writeExistingConfig(t, stub.configFile, existing)

	out, errText, code := runAllod(t, "site", "config", "update", "user")
	if code != 1 || !strings.Contains(errText, "more than one user key") {
		t.Errorf("exit=%d stdout=%q stderr=%q, want duplicate-user refusal", code, out, errText)
	}
	if got := readFile(t, stub.configFile); got != existing {
		t.Errorf("ambiguous update modified the config: %q", got)
	}
	if stub.askCalls != 0 || len(stub.fieldCalls) != 0 {
		t.Errorf("ambiguous update prompted: full=%d fields=%v", stub.askCalls, stub.fieldCalls)
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

// An explicit location is authoritative: config neither asks rclone where its
// default lives nor writes the path rclone would have reported. Unlike deploy,
// config may create a missing named file because creating the credential is its
// purpose.
func TestSiteConfigUsesExplicitPathWithoutResolvingRcloneDefault(t *testing.T) {
	stub := answeringStub(t)
	useConfigStub(t, stub)
	namedPath := filepath.Join(t.TempDir(), "declarative staging", "rclone.conf")

	out, errText, code := runAllod(t, "site", "config", "--config="+namedPath)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, errText)
	}
	if !strings.Contains(readFile(t, namedPath), "[shared]\n") {
		t.Errorf("%s does not hold the remote", namedPath)
	}
	if _, err := os.Stat(stub.configFile); !os.IsNotExist(err) {
		t.Errorf("rclone's reported default was touched (err = %v)", err)
	}
	for _, call := range stub.runs {
		if strings.Join(call.args, " ") == "config file" {
			t.Errorf("explicit --config still ran 'rclone config file': %+v", stub.runs)
		}
	}
	if !strings.Contains(out, "Config: "+namedPath+"\n") {
		t.Errorf("stdout does not report the named path\ngot: %q", out)
	}
	if !strings.Contains(out, "allod site check") || !strings.Contains(out, "Config path above with --config") {
		t.Errorf("stdout does not give truthful named-config check advice\ngot: %q", out)
	}
}

// The shared selector remains free-order around lifecycle subcommands. A
// named update neither resolves nor touches rclone's default configuration.
func TestSiteConfigUpdateUsesExplicitPathInEitherOrder(t *testing.T) {
	orders := [][]string{
		{"--config", "NAMED", "update", "user"},
		{"update", "user", "--config=NAMED"},
	}
	for index, order := range orders {
		t.Run(fmt.Sprintf("order %d", index+1), func(t *testing.T) {
			stub := answeringStub(t)
			useConfigStub(t, stub)
			namedPath := filepath.Join(t.TempDir(), "selected rclone.conf")
			writeExistingConfig(t, namedPath,
				"[shared]\ntype = ftp\nhost = old.example\nuser = old-user\npass = OLD\nexplicit_tls = true\n")
			args := []string{"site", "config"}
			for _, arg := range order {
				if arg == "NAMED" {
					arg = namedPath
				} else if arg == "--config=NAMED" {
					arg = "--config=" + namedPath
				}
				args = append(args, arg)
			}

			out, errText, code := runAllod(t, args...)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0\nstderr: %s", code, errText)
			}
			if !strings.Contains(readFile(t, namedPath), "user = "+testUser+"\n") {
				t.Errorf("named config did not receive the update")
			}
			if _, err := os.Stat(stub.configFile); !os.IsNotExist(err) {
				t.Errorf("default config was touched (err = %v)", err)
			}
			for _, call := range stub.runs {
				if strings.Join(call.args, " ") == "config file" {
					t.Errorf("named update resolved rclone's default: %+v", stub.runs)
				}
			}
			if len(stub.fieldCalls) != 1 || stub.fieldCalls[0] != remoteConfigUser {
				t.Errorf("field prompts = %v, want [user]", stub.fieldCalls)
			}
			if !strings.Contains(out, "Config: "+namedPath+"\n") || !strings.Contains(out, "Config path above with --config") {
				t.Errorf("stdout loses named-path check guidance: %q", out)
			}
		})
	}
}

func TestSiteConfigLeadingDashPathUsesEqualsSpelling(t *testing.T) {
	stub := answeringStub(t)
	useConfigStub(t, stub)
	t.Chdir(t.TempDir())

	out, errText, code := runAllod(t, "site", "config", "--config=-runtime.conf")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr: %s", code, errText)
	}
	if !strings.Contains(readFile(t, "-runtime.conf"), "[shared]\n") {
		t.Error("leading-dash config path does not hold the remote")
	}
	if !strings.Contains(out, "Config: -runtime.conf\n") {
		t.Errorf("stdout does not report the leading-dash path: %q", out)
	}
}

func TestSiteConfigRejectsUnreadableNamedFileBeforePrompting(t *testing.T) {
	stub := answeringStub(t)
	useConfigStub(t, stub)
	namedPath := filepath.Join(t.TempDir(), "unreadable.conf")
	existing := "[shared]\npass = secret\n"
	writeExistingConfig(t, namedPath, existing)
	if err := os.Chmod(namedPath, 0000); err != nil {
		t.Fatalf("could not make named config unreadable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(namedPath, 0600) })
	if file, err := os.Open(namedPath); err == nil {
		file.Close()
		t.Skip("test account can still open a mode-000 file")
	}

	_, errText, code := runAllod(t, "site", "config", "--config", namedPath)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errText, "could not read "+namedPath) {
		t.Errorf("stderr does not identify the unreadable named path\ngot: %q", errText)
	}
	if strings.Contains(errText, existing) {
		t.Errorf("stderr contains config contents: %q", errText)
	}
	if stub.askCalls != 0 {
		t.Errorf("the operator was asked %d times before the refusal, want 0", stub.askCalls)
	}
	for _, call := range stub.runs {
		if strings.Join(call.args, " ") == "config file" || strings.Join(call.args, " ") == "obscure -" {
			t.Errorf("rclone ran before the unreadable-file refusal: %+v", stub.runs)
		}
	}
}

// A symlink may be inspected but not mutated: replacing its directory entry
// would sever an activation-managed name from its generation-specific target.
func TestSiteConfigTreatsSymlinkAsReadOnly(t *testing.T) {
	stub := answeringStub(t)
	useConfigStub(t, stub)
	dir := t.TempDir()
	target := filepath.Join(dir, "generation-1.conf")
	selected := filepath.Join(dir, "active.conf")
	existing := "[shared]\ntype = ftp\nhost = activation.example\nuser = activation-user\npass = activation-owned\nexplicit_tls = true\n"
	writeExistingConfig(t, target, existing)
	if err := os.Symlink(target, selected); err != nil {
		t.Fatalf("could not create config symlink: %v", err)
	}

	if got, err := readRcloneConfig(selected); err != nil || got != existing {
		t.Fatalf("read-only config access through symlink = %q, %v", got, err)
	}
	out, errText, code := runAllod(t, "site", "config", "show", "--config", selected)
	if code != 0 || errText != "" {
		t.Fatalf("show through activation symlink: code=%d stderr=%q", code, errText)
	}
	for _, want := range []string{"Config: " + selected, "Type: ftp", "Host: activation.example", "User: activation-user"} {
		if !strings.Contains(out, want) {
			t.Errorf("safe symlink inspection does not contain %q: %q", want, out)
		}
	}
	if strings.Contains(out, "activation-owned") {
		t.Errorf("symlink inspection prints the obscured password: %q", out)
	}

	mutations := [][]string{
		{"site", "config", "--config", selected},
		{"site", "config", "replace", "--config", selected},
		{"site", "config", "--config", selected, "--force"},
		{"site", "config", "update", "user", "--config", selected},
	}
	for _, args := range mutations {
		_, errText, code = runAllod(t, args...)
		if code != 1 || !strings.Contains(errText, "symbolic links are read-only") {
			t.Errorf("%v symlink refusal: code=%d stderr=%q", args[2:], code, errText)
		}
	}
	if stub.askCalls != 0 || len(stub.fieldCalls) != 0 {
		t.Errorf("the operator was prompted before symlink refusals: full=%d fields=%v", stub.askCalls, stub.fieldCalls)
	}
	if info, err := os.Lstat(selected); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("selected path is no longer a symlink: info=%v err=%v", info, err)
	}
	if got := readFile(t, target); got != existing {
		t.Errorf("activation target changed to %q", got)
	}
}

func TestSiteConfigRefusesNonRegularFileBeforePrompting(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{"directory", func(t *testing.T, path string) {
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatalf("could not create config directory: %v", err)
			}
		}},
		{"FIFO", func(t *testing.T, path string) {
			if err := syscall.Mkfifo(path, 0600); err != nil {
				t.Fatalf("could not create config FIFO: %v", err)
			}
		}},
	}

	mutations := [][]string{
		{},
		{"replace"},
		{"update", "password"},
	}
	for _, test := range tests {
		for _, mutation := range mutations {
			t.Run(test.name+" "+strings.Join(mutation, " "), func(t *testing.T) {
				stub := answeringStub(t)
				useConfigStub(t, stub)
				path := filepath.Join(t.TempDir(), "rclone.conf")
				test.setup(t, path)
				args := append([]string{"site", "config"}, mutation...)
				args = append(args, "--config", path)

				_, errText, code := runAllod(t, args...)
				if code != 1 || !strings.Contains(errText, "not a regular file") {
					t.Errorf("non-regular refusal: code=%d stderr=%q", code, errText)
				}
				if stub.askCalls != 0 || len(stub.fieldCalls) != 0 {
					t.Errorf("the operator was prompted before refusal: full=%d fields=%v", stub.askCalls, stub.fieldCalls)
				}
			})
		}
	}
}

// The writer repeats the type check after the prompts. A path that was missing
// during preflight but becomes an activation symlink is refused, not renamed
// over after the operator has supplied a password.
func TestSiteConfigRefusesSymlinkIntroducedWhilePrompting(t *testing.T) {
	stub := answeringStub(t)
	useConfigStub(t, stub)
	dir := t.TempDir()
	target := filepath.Join(dir, "generation-1.conf")
	selected := filepath.Join(dir, "active.conf")
	existing := "[shared]\npass = activation-owned\n"
	writeExistingConfig(t, target, existing)
	siteAsk = func() (string, string, string, error) {
		stub.askCalls++
		if err := os.Symlink(target, selected); err != nil {
			t.Fatalf("could not rotate selected path during prompt: %v", err)
		}
		return stub.host, stub.user, stub.password, nil
	}

	_, errText, code := runAllod(t, "site", "config", "--config", selected)
	if code != 1 || !strings.Contains(errText, "symbolic links are read-only") {
		t.Errorf("rotated-path refusal: code=%d stderr=%q", code, errText)
	}
	if stub.askCalls != 1 {
		t.Errorf("answers requested %d times, want 1", stub.askCalls)
	}
	if info, err := os.Lstat(selected); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("selected path is no longer a symlink: info=%v err=%v", info, err)
	}
	if got := readFile(t, target); got != existing {
		t.Errorf("activation target changed to %q", got)
	}
}

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

func TestSiteConfigRejectsUnsafeResolvedPathWithoutPrintingIt(t *testing.T) {
	stub := answeringStub(t)
	stub.configFile = "/tmp/rclone\x1b[2JFORGED.conf"
	useConfigStub(t, stub)

	out, errText, code := runAllod(t, "site", "config", "show")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if out != "" || !strings.Contains(errText, "rclone returned an unusable path") {
		t.Errorf("stdout=%q stderr=%q, want safe resolved-path refusal", out, errText)
	}
	if strings.Contains(errText, "\x1b") || strings.Contains(errText, "FORGED") {
		t.Errorf("stderr prints the unsafe path: %q", errText)
	}
	if stub.askCalls != 0 || len(stub.fieldCalls) != 0 {
		t.Errorf("unsafe path prompted: full=%d fields=%v", stub.askCalls, stub.fieldCalls)
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
		{[]string{"site", "config", "--config"}, "--config requires a path for site config"},
		{[]string{"site", "config", "--config", "--force"}, "use --config=<path> when the path begins with '-'"},
		{[]string{"site", "config", "--config="}, "--config requires a non-empty path for site config"},
		{[]string{"site", "config", "--config=/run/credential\nallod: forged"}, "--config path must be one line"},
		{[]string{"site", "config", "--config", "/one", "--config", "/two"}, "--config may only be specified once"},
		{[]string{"site", "config", "update"}, "requires exactly one field"},
		{[]string{"site", "config", "update", "port"}, "unknown field"},
		{[]string{"site", "config", "update", "user", "password"}, "takes one field"},
		{[]string{"site", "config", "show", "user"}, "unexpected argument for site config show"},
		{[]string{"site", "config", "replace", "user"}, "unexpected argument for site config replace"},
		{[]string{"site", "config", "replace", "--force"}, "--force cannot be combined"},
		{[]string{"site", "config", "show", "--force"}, "--force cannot be combined"},
		{[]string{"site", "config", "--force", "--force"}, "--force may only be specified once"},
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

// An unsafe path is rejected by the shared option parser, before rclone path
// resolution or any credential prompt. The diagnostic does not repeat the
// hostile bytes, so none can become a forged terminal line.
func TestSiteConfigRejectsUnsafeUnicodeConfigPathsBeforeEffects(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"invalid UTF-8", "/run/credentials/" + string([]byte{0xff}) + "forged"},
		{"C1 control", "/run/credentials/\u0085forged"},
		{"line separator", "/run/credentials/\u2028forged"},
		{"paragraph separator", "/run/credentials/\u2029forged"},
	}
	wantErr := "allod: --config path must be one line of printable text for site config\n"

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := answeringStub(t)
			useConfigStub(t, stub)

			out, errText, code := runAllod(t, "site", "config", "--config="+test.path)
			if code != 1 || out != "" || errText != wantErr {
				t.Errorf("unsafe path result: code=%d stdout=%q stderr=%q", code, out, errText)
			}
			if stub.askCalls != 0 || len(stub.runs) != 0 {
				t.Errorf("effects after unsafe path: prompts=%d commands=%+v", stub.askCalls, stub.runs)
			}
			if strings.Contains(out+errText, "forged") || strings.Contains(out+errText, test.path) {
				t.Errorf("unsafe path reached command output: stdout=%q stderr=%q", out, errText)
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
		{"invalid UTF-8 in the host", "ftp." + string([]byte{0xff}) + "forged", testUser, testPassword, "host must be one line"},
		{"C1 control in the user", testHost, "user\u0085forged", testPassword, "user must be one line"},
		{"line separator in the host", "ftp\u2028forged", testUser, testPassword, "host must be one line"},
		{"paragraph separator in the user", testHost, "user\u2029forged", testPassword, "user must be one line"},
		{"empty password", testHost, testUser, "", "password must not be empty"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := answeringStub(t)
			stub.host, stub.user, stub.password = test.host, test.user, test.password
			useConfigStub(t, stub)

			out, errText, code := runAllod(t, "site", "config")
			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if !strings.Contains(errText, test.errHas) {
				t.Errorf("stderr does not contain %q\ngot: %q", test.errHas, errText)
			}
			if _, err := os.Stat(stub.configFile); err == nil {
				t.Errorf("%s was written despite the refusal", stub.configFile)
			}
			if strings.Contains(test.host+test.user, "forged") && strings.Contains(out+errText, "forged") {
				t.Errorf("unsafe answer reached command output: stdout=%q stderr=%q", out, errText)
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

func TestSiteConfigRejectsUnsafeUnicodeFromRcloneObscure(t *testing.T) {
	tests := []struct {
		name     string
		obscured string
	}{
		{"invalid UTF-8", "safe" + string([]byte{0xff}) + "forged"},
		{"C1 control", "safe\u0085forged"},
		{"line separator", "safe\u2028forged"},
		{"paragraph separator", "safe\u2029forged"},
	}
	wantErr := "allod: could not obscure the password: 'rclone obscure -' printed something that is not one config value\n"

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stub := answeringStub(t)
			stub.obscured = test.obscured
			useConfigStub(t, stub)

			out, errText, code := runAllod(t, "site", "config")
			if code != 1 || out != "" || errText != wantErr {
				t.Errorf("unsafe obscure result: code=%d stdout=%q stderr=%q", code, out, errText)
			}
			if strings.Contains(out+errText, "forged") || strings.Contains(out+errText, test.obscured) {
				t.Errorf("unsafe obscure output reached command output: stdout=%q stderr=%q", out, errText)
			}
			if _, err := os.Stat(stub.configFile); !os.IsNotExist(err) {
				t.Errorf("config was written after unsafe obscure output (err = %v)", err)
			}
		})
	}
}

// --- The pieces ---

func TestValidConfigValue(t *testing.T) {
	valid := []string{"ftp.example.com", "user@example", "a space is fine", "ünïcode"}
	invalid := []string{
		"",
		"two\nlines",
		"a\ttab",
		"a\rreturn",
		"nul\x00byte",
		"bell\x07",
		string([]byte{0xff}),
		"next\u0085line",
		"next\u2028line",
		"next\u2029paragraph",
	}
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
