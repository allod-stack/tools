//go:build site

package main

// 'allod site config' creates the one rclone remote a deploy needs, so that
// setting a machine up is three answers rather than a walk through rclone's
// own interactive configurator — where the operator has to know that the
// backend is 'ftp', that the option is spelled explicit_tls, and that the
// remote must be called 'shared' for anything here to find it.
//
// Two rules shape the whole file. The password is never an argument, because
// arguments are world-readable in /proc/<pid>/cmdline for as long as the
// process lives; it goes to 'rclone obscure -' on stdin instead. And the
// answer is never printed, in plaintext or obscured — rclone's obscuring is
// reversible by anyone with rclone, so the obscured value is exactly as
// sensitive as the password and belongs only in a 0600 file.
//
// Note that 'config' here means rclone's configuration. The siteConfig type in
// site.go is the parsed site.toml, which is a different thing entirely.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// The rclone configuration file holds reversible credentials, so it is created
// no wider than its owner and is put back that way on every write, whatever
// mode it had before.
const (
	rcloneConfigDirMode  = 0700
	rcloneConfigFileMode = 0600
)

// Test seams for 'site config'.
//
// siteRun is the exec seam, and it is deliberately argv-shaped rather than
// one seam per command: a test can then assert what this command puts on a
// command line, which is the property that keeps the password off it.
//
// siteAsk is the terminal seam. It collects all three answers in one call
// because the terminal must be opened once — a buffered reader over /dev/tty
// may read past the line it returns, and a second reader would lose whatever
// the first one had buffered, which is how a pasted password disappears.
var (
	siteRun = runCapture
	siteAsk = askRemoteCredentials
)

// rcloneConfigSelection is the one config-file selector shared by every site
// command that reads or writes the hosting credential. Keeping the parser and
// rclone argv construction here gives later credential lifecycle commands the
// same --config contract without each command inventing its own precedence.
//
// With no explicit path, rclone resolves RCLONE_CONFIG and its platform default
// itself. An explicit path is passed as --config and therefore wins over both.
type rcloneConfigSelection struct {
	path     string
	explicit bool
}

// consume parses the shared --config option at the front of args. It accepts
// both conventional spellings, leaves every other argument for the command's
// own parser, and refuses ambiguity from naming the option twice. A separated
// value may not look like another option: --config=<path> is the unambiguous
// spelling for the unusual case where a relative path begins with a dash.
func (selection *rcloneConfigSelection) consume(args []string, command string) ([]string, bool) {
	if len(args) == 0 {
		return args, false
	}

	var path string
	switch {
	case args[0] == "--config":
		if len(args) < 2 {
			die(1, "--config requires a path for site %s", command)
		}
		if strings.HasPrefix(args[1], "-") {
			die(1, "--config requires a path for site %s; use --config=<path> when the path begins with '-'", command)
		}
		path, args = args[1], args[2:]
	case strings.HasPrefix(args[0], "--config="):
		path, args = strings.TrimPrefix(args[0], "--config="), args[1:]
	default:
		return args, false
	}

	if path == "" {
		die(1, "--config requires a non-empty path for site %s", command)
	}
	if !validConfigValue(path) {
		die(1, "--config path must be one line of printable text for site %s", command)
	}
	if selection.explicit {
		die(1, "--config may only be specified once for site %s", command)
	}
	selection.path, selection.explicit = path, true
	return args, true
}

// configPath is the contract used by commands that hand the selection to
// rclone: an empty value means rclone resolves its own configuration.
func (selection rcloneConfigSelection) configPath() string {
	if !selection.explicit {
		return ""
	}
	return selection.path
}

// rcloneArgs is the only place that turns a selected path into child argv. The
// explicit flag is placed before the command name, where rclone treats it as a
// global option. An empty path adds no flag, preserving rclone's own resolution.
func rcloneArgs(configPath string, args ...string) []string {
	if configPath == "" {
		return append([]string(nil), args...)
	}
	return append([]string{"--config", configPath}, args...)
}

// requireReadable checks a declaratively supplied config before a deploy does
// any expensive work. It opens rather than reads the file: that proves the
// caller can read it without bringing a plaintext-equivalent credential into
// this process. The nonblocking opener follows a final symlink so an
// activation-managed path can point at a regular credential, but refuses the
// FIFO or other non-regular target that could otherwise block this process.
func (selection rcloneConfigSelection) requireReadable() error {
	if !selection.explicit {
		return nil
	}
	file, err := openReadableRcloneConfig(selection.path)
	if err != nil {
		return err
	}
	return file.Close()
}

func siteConfigure(args []string) {
	force := false
	var configSelection rcloneConfigSelection
	for len(args) > 0 {
		if rest, consumed := configSelection.consume(args, "config"); consumed {
			args = rest
			continue
		}
		switch args[0] {
		case "--force":
			force, args = true, args[1:]
		case "-h", "--help":
			fmt.Fprint(stdout, siteUsageText)
			return
		default:
			// Nothing on the command line names the remote, its type, or its
			// TLS setting. A deploy targets 'shared' and nothing else, so a
			// remote under any other name would be one nothing ever reads.
			if strings.HasPrefix(args[0], "-") {
				die(1, "unknown option for site config: %s", args[0])
			}
			die(1, "unexpected argument for site config: %s", args[0])
		}
	}

	if _, err := exec.LookPath("rclone"); err != nil {
		die(1, "'rclone' not found on PATH")
	}

	path, err := rcloneConfigPath(configSelection)
	if err != nil {
		die(1, "could not determine where rclone keeps its configuration: %s", err)
	}
	existing, err := readRcloneConfigForUpdate(path)
	if err != nil {
		if errors.Is(err, errRcloneConfigSymlink) || errors.Is(err, errRcloneConfigNotRegular) {
			die(1, "refusing to update %s: %s", path, err)
		}
		die(1, "could not read %s: %s", path, err)
	}
	// Asked before the prompts rather than after them: nobody should type a
	// password only to be told the command was never going to store it.
	if hasStanza(existing, siteRemoteName) && !force {
		fmt.Fprintf(stderr, "allod: a '%s' rclone remote already exists in %s\n", siteRemoteName, path)
		fmt.Fprintln(stderr, "allod: while resolving: whether to replace a credential that may be working")
		fmt.Fprintln(stderr, "allod: to fix: rerun with --force to replace it, or leave it as it is")
		exit(1)
	}

	host, user, password, err := siteAsk()
	if err != nil {
		die(1, "could not read the answers: %s", err)
	}
	// A newline in either of these would end the config line and turn the rest
	// of the answer into further rclone directives, in a file whose other
	// stanzas may point at other accounts.
	if !validConfigValue(host) {
		die(1, "the host must be one line of printable text")
	}
	if !validConfigValue(user) {
		die(1, "the user must be one line of printable text")
	}
	if password == "" {
		die(1, "the password must not be empty")
	}

	obscured, err := obscurePassword(password)
	if err != nil {
		die(1, "could not obscure the password: %s", err)
	}

	updated := replaceStanza(existing, siteRemoteName, remoteStanza(host, user, obscured))
	if err := writeRcloneConfig(path, updated); err != nil {
		die(1, "could not write %s: %s", path, err)
	}

	// Everything printed here is safe to read over a shoulder or paste into an
	// issue. The password and its obscured form are not, and are not printed.
	fmt.Fprintf(stdout, "Remote: %s\nType: ftp\nHost: %s\nUser: %s\nConfig: %s\n", siteRemoteName, host, user, path)
	fmt.Fprintln(stdout, "The next deploy will check this remote before building.")
}

// runCapture runs a command with input on its stdin and returns its trimmed
// stdout and exit status. Stderr is passed through, so a failure explains
// itself in the child's own words.
func runCapture(input, name string, args ...string) (string, int) {
	var out bytes.Buffer
	cmd := exec.Command(name, args...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	cmd.Stdout = &out
	cmd.Stderr = stderr
	status := commandExitCode(cmd.Run())
	return strings.TrimRight(out.String(), "\n"), status
}

// obscurePassword returns the password in the only form rclone will read out
// of a config file. The dash is the whole point: 'rclone obscure <password>'
// would publish the plaintext in the process table, where every account on the
// machine can read it, so the plaintext goes on stdin and nowhere else.
//
// It is written without a trailing newline, which is right whether rclone
// reads one line from stdin or all of it: end of file ends the read either
// way, and a trailing byte rclone did not strip would become part of the
// stored password — a remote that fails to authenticate for a reason nothing
// on the screen would explain.
func obscurePassword(password string) (string, error) {
	obscured, status := siteRun(password, "rclone", "obscure", "-")
	if status != 0 {
		return "", fmt.Errorf("'rclone obscure -' exited %d", status)
	}
	obscured = strings.TrimSpace(obscured)
	if obscured == "" {
		return "", errors.New("'rclone obscure -' printed nothing")
	}
	if !validConfigValue(obscured) {
		return "", errors.New("'rclone obscure -' printed something that is not one config value")
	}
	return obscured, nil
}

// remoteStanza renders the remote in rclone's own configuration syntax.
// explicit_tls is what makes this FTPS rather than FTP: the credential is
// negotiated under TLS with AUTH TLS on the ordinary port, instead of being
// sent across the wire in the clear.
func remoteStanza(host, user, obscured string) string {
	return fmt.Sprintf("[%s]\ntype = ftp\nhost = %s\nuser = %s\npass = %s\nexplicit_tls = true\n",
		siteRemoteName, host, user, obscured)
}

// validConfigValue rejects anything that could not be one rclone config value:
// an empty answer, and any control character. See the call sites for why a
// newline in particular has to be refused rather than escaped.
func validConfigValue(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// rcloneConfigPath returns an explicit selection unchanged. Otherwise it asks
// rclone where its configuration lives, because the answer depends on the
// build, on RCLONE_CONFIG, and on XDG_CONFIG_HOME, and a stanza written to the
// wrong file is a remote rclone never reads. 'rclone config file' prints a
// sentence and then the path, so the path is the last non-blank line. If
// rclone cannot answer — it is old, or it fails because there is no
// configuration yet — the XDG default is used, which is where rclone itself
// would create one.
func rcloneConfigPath(selection rcloneConfigSelection) (string, error) {
	if selection.explicit {
		return selection.path, nil
	}
	if out, status := siteRun("", "rclone", "config", "file"); status == 0 {
		if path := lastNonBlankLine(out); filepath.IsAbs(path) {
			return path, nil
		}
	}
	return defaultRcloneConfigPath()
}

func defaultRcloneConfigPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home := homeDir()
		if home == "" {
			return "", errors.New("neither XDG_CONFIG_HOME nor HOME is set")
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "rclone", "rclone.conf"), nil
}

func lastNonBlankLine(text string) string {
	lines := strings.Split(text, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if line := strings.TrimSpace(lines[index]); line != "" {
			return line
		}
	}
	return ""
}

var (
	errRcloneConfigNotRegular = errors.New("not a regular file")
	errRcloneConfigSymlink    = errors.New("symbolic links are read-only; refusing to replace one")
)

// openReadableRcloneConfig is the read-only contract shared by deploy and
// credential inspection. O_NONBLOCK keeps a FIFO from hanging the command;
// checking the opened descriptor closes the stat/open race. A final symlink is
// followed deliberately so activation can point a stable name at a regular,
// generation-specific credential.
func openReadableRcloneConfig(path string) (*os.File, error) {
	return openRegularRcloneConfig(path, false)
}

// openMutableRcloneConfig is the update contract. A symlink is a read-only
// source: atomically replacing its directory entry would silently sever it
// from the activation-managed target. Missing paths are handled by the caller.
func openMutableRcloneConfig(path string) (*os.File, error) {
	return openRegularRcloneConfig(path, true)
}

func openRegularRcloneConfig(path string, noFollow bool) (*os.File, error) {
	flags := os.O_RDONLY | syscall.O_NONBLOCK
	if noFollow {
		flags |= syscall.O_NOFOLLOW
	}
	file, err := os.OpenFile(path, flags, 0)
	if err != nil {
		if noFollow && errors.Is(err, syscall.ELOOP) {
			return nil, errRcloneConfigSymlink
		}
		return nil, err
	}
	info, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return nil, statErr
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errRcloneConfigNotRegular
	}
	return file, nil
}

// readRcloneConfig is the read-only lifecycle helper. It follows a final
// symlink to a regular file and reports a missing path, which lets a future
// 'show' command distinguish an absent configuration from an empty one.
func readRcloneConfig(path string) (string, error) {
	return readOpenedRcloneConfig(path, openReadableRcloneConfig)
}

// readRcloneConfigForUpdate reads only a regular file owned by this path. A
// missing path is an empty starting point because 'site config' may create it;
// symlinks and other existing file types are refused before any prompt.
func readRcloneConfigForUpdate(path string) (string, error) {
	text, err := readOpenedRcloneConfig(path, openMutableRcloneConfig)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return text, err
}

func readOpenedRcloneConfig(path string, open func(string) (*os.File, error)) (string, error) {
	file, err := open(path)
	if err != nil {
		return "", err
	}
	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil {
		return "", readErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return string(data), nil
}

// requireMutableRcloneConfigPath protects the atomic writer as well as its
// callers. Site config opens the path through openMutableRcloneConfig before
// prompting; the writer checks again before creating its temporary file and
// immediately before rename so later replacements are refused too.
func requireMutableRcloneConfigPath(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errRcloneConfigSymlink
	}
	if !info.Mode().IsRegular() {
		return errRcloneConfigNotRegular
	}
	return nil
}

// writeRcloneConfig replaces the configuration file atomically: a temporary
// file in the same directory, then a rename. A half-written rclone config is
// a machine that has lost every remote it had, and truncating in place makes
// that the outcome of any interruption.
func writeRcloneConfig(path, text string) error {
	if err := requireMutableRcloneConfigPath(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, rcloneConfigDirMode); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".rclone.conf-allod-")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)

	if _, err := temp.WriteString(text); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	// os.CreateTemp already creates the file at 0600; this says so out loud,
	// and fixes the mode of a file rclone or an operator left wider.
	if err := os.Chmod(name, rcloneConfigFileMode); err != nil {
		return err
	}
	if err := requireMutableRcloneConfigPath(path); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// hasStanza reports whether text already holds a [name] section.
func hasStanza(text, name string) bool {
	_, _, ok := findStanza(strings.Split(text, "\n"), name)
	return ok
}

// findStanza returns the half-open line range of the [name] section, which
// runs from its header to the next header or to the end of the file — exactly
// as rclone reads it.
func findStanza(lines []string, name string) (start, end int, ok bool) {
	header := "[" + name + "]"
	start = -1
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if start < 0 {
			if trimmed == header {
				start = index
			}
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			return start, index, true
		}
	}
	if start < 0 {
		return 0, 0, false
	}
	return start, len(lines), true
}

// replaceStanza returns text with the [name] section replaced by stanza, or
// with stanza appended if there is none. Every other byte of the file survives:
// the rclone configuration is machine-wide, the other remotes in it belong to
// whoever put them there, and this command has no opinion about them.
func replaceStanza(text, name, stanza string) string {
	if strings.TrimSpace(text) == "" {
		return stanza
	}
	lines := strings.Split(text, "\n")
	start, end, ok := findStanza(lines, name)
	if !ok {
		return terminateLine(text) + "\n" + stanza
	}
	kept := append([]string{}, lines[:start]...)
	kept = append(kept, strings.Split(stanza, "\n")...)
	kept = append(kept, lines[end:]...)
	return terminateLine(strings.Join(kept, "\n"))
}

func terminateLine(text string) string {
	if text == "" || strings.HasSuffix(text, "\n") {
		return text
	}
	return text + "\n"
}

// askRemoteCredentials asks the three questions on the controlling terminal.
//
// The terminal is used rather than the stdin seam because a prompt answered by
// a pipe is a prompt the operator never saw, and the answer to the third one
// is a password: it is read with echo off, so it does not reach the screen,
// the scrollback, or a screen share.
func askRemoteCredentials() (host, user, password string, err error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", "", "", fmt.Errorf("no terminal to ask on: %w", err)
	}
	defer tty.Close()
	reader := bufio.NewReader(tty)

	if host, err = askLine(tty, reader, "FTP host: "); err != nil {
		return "", "", "", err
	}
	if user, err = askLine(tty, reader, "FTP user: "); err != nil {
		return "", "", "", err
	}
	if password, err = askSecret(tty, reader, "FTP password: "); err != nil {
		return "", "", "", err
	}
	return host, user, password, nil
}

func askLine(tty *os.File, reader *bufio.Reader, prompt string) (string, error) {
	fmt.Fprint(tty, prompt)
	line, err := reader.ReadString('\n')
	// A terminal closed mid-answer still returns what was typed before it
	// went; only an EOF with nothing before it is a failure to answer.
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func askSecret(tty *os.File, reader *bufio.Reader, prompt string) (string, error) {
	restore, err := disableEcho(tty)
	if err != nil {
		return "", err
	}
	defer restore()
	secret, err := askLine(tty, reader, prompt)
	// The Return that ended the answer was not echoed either, so the next
	// thing printed would land on the prompt line.
	fmt.Fprintln(tty)
	return secret, err
}

// disableEcho turns the terminal's echo off for the duration of one prompt,
// the way 'stty -echo' does, and returns the function that puts it back. The
// standard library has no terminal package, so this is the TCGETS/TCSETS
// ioctl pair directly — the same call cmd/forge makes to answer isatty.
//
// The restore also runs on SIGINT. Ctrl-C at a password prompt is an ordinary
// thing to do, and the default action would kill the process with echo still
// off, leaving the operator typing blind into their own shell afterwards.
func disableEcho(tty *os.File) (func(), error) {
	var original syscall.Termios
	if err := getTermios(tty, &original); err != nil {
		return nil, fmt.Errorf("could not read the terminal settings: %w", err)
	}
	quiet := original
	quiet.Lflag &^= syscall.ECHO
	if err := setTermios(tty, &quiet); err != nil {
		return nil, fmt.Errorf("could not turn off terminal echo: %w", err)
	}

	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt)
	done := make(chan struct{})
	go func() {
		select {
		case <-interrupted:
			_ = setTermios(tty, &original)
			// 128 + SIGINT: what a shell reports for a command its user
			// interrupted, which is what happened.
			os.Exit(130)
		case <-done:
		}
	}()

	return func() {
		signal.Stop(interrupted)
		close(done)
		_ = setTermios(tty, &original)
	}, nil
}

func getTermios(tty *os.File, out *syscall.Termios) error {
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, tty.Fd(),
		uintptr(syscall.TCGETS), uintptr(unsafe.Pointer(out)), 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func setTermios(tty *os.File, in *syscall.Termios) error {
	_, _, errno := syscall.Syscall6(syscall.SYS_IOCTL, tty.Fd(),
		uintptr(syscall.TCSETS), uintptr(unsafe.Pointer(in)), 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
