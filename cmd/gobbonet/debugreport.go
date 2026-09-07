package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/ElodineOfficial/GobboNet/internal/debugreport"
)

// cmdDebugReport gathers the "must gather" packet from the command line.
//
// Deliberately unauthenticated. It reads config.toml off disk and probes the
// upstream itself, so it works on an install that will not start, and on one
// whose password the user has lost — which are two of the situations most in
// need of a report. The cost of that choice is the runtime section: supervisor
// state and job history live in the server process and are only reachable
// through the gated endpoint, so this command says so rather than pretending
// the information does not exist.
//
// It reads no conversation data. The state files are counted, never opened for
// content (internal/debugreport/data.go), and the test message below is a fixed
// string in the collector, not anything the user has written.
func cmdDebugReport(argv []string) error {
	fs := flag.NewFlagSet("gobbonet debug-report", flag.ContinueOnError)
	configPath := stringFlag(fs, "config", "path to config.toml")
	out := stringFlag(fs, "out", "write the report to this file instead of stdout")
	asJSON := fs.Bool("json", false, "emit JSON instead of markdown")
	noProbe := fs.Bool("no-probe", false, "skip the connectivity probes")
	redactHome := fs.Bool("redact-home", false, "replace your home directory with ~ throughout")
	yes := fs.Bool("yes", false, "answer yes to the test-message prompt (implies a live request)")
	noTest := fs.Bool("no-test", false, "never send a test message, and do not ask")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}

	// Written to stderr so that `gobbonet debug-report > report.md` still
	// produces a clean file while the user watches progress.
	fmt.Fprintln(os.Stderr, "  [..] Collecting build, system, config and model state...")

	opts := debugreport.Options{
		Cfg:         cfg,
		Tier:        debugreport.TierFull,
		Session:     debugreport.SessionNotApplicable,
		Probe:       !*noProbe,
		RedactHome:  *redactHome,
		RuntimeNote: runtimeNote(cfg.ListenHost, cfg.ListenPort),
	}

	if !*noProbe {
		opts.TestPrompt = askTestMessage(cfg.LLMURL, *yes, *noTest)
	}

	if opts.Probe {
		fmt.Fprintln(os.Stderr, "  [..] Probing "+cfg.LLMURL+" ...")
	}
	report := debugreport.Collect(context.Background(), opts)

	rendered, err := render(report, *asJSON)
	if err != nil {
		return err
	}

	if *out == "" {
		fmt.Print(rendered)
		return nil
	}
	// 0600: the report names paths, hostnames and the machine's own addresses.
	// None of it is secret, but it is nobody else's business by default either.
	if err := os.WriteFile(*out, []byte(rendered), 0o600); err != nil {
		return fmt.Errorf("could not write %s: %w", *out, err)
	}
	fmt.Fprintf(os.Stderr, "  [OK] Report written to %s\n", *out)
	fmt.Fprintln(os.Stderr, "       Paste its contents into the issue you are opening.")
	return nil
}

func render(r debugreport.Report, asJSON bool) (string, error) {
	if !asJSON {
		return r.Markdown(), nil
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", fmt.Errorf("could not encode the report: %w", err)
	}
	return string(b) + "\n", nil
}

// askTestMessage decides whether to send a live generation.
//
// Default no, and the prompt says what the request costs before asking. It is
// an outbound call that generates a token, and against a metered API endpoint
// that is somebody's money — small, but not mine to spend silently. A
// non-interactive run without --yes declines, because there is nobody there to
// have consented.
func askTestMessage(llmURL string, yes, noTest bool) bool {
	if noTest {
		return false
	}
	if yes {
		return true
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintln(os.Stderr,
			"  [*] Not a terminal, so the live test message was skipped. Pass --yes to include it.")
		return false
	}

	fmt.Fprintf(os.Stderr, `
  The report can finish with a live test: one message to the LLM at
      %s
  asking for a single token. It proves the whole path end to end and turns
  "Error: 502" into an actual error you can read. It sends nothing from your
  conversations -- the prompt is a fixed string.

  It does make one outbound request, which costs money on a metered API.

`, llmURL)
	fmt.Fprint(os.Stderr, "  Send a test message to the LLM? [y/N]: ")

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// runtimeNote explains the missing runtime section accurately.
//
// "No server is running" and "a server is running but this command does not ask
// it for anything" are different situations with different next steps, and a
// report that confused them would send a user looking for a process that is
// right there. One connect settles it.
func runtimeNote(host string, port int) string {
	const base = "runtime: not collected. Supervisor state, llama-server's captured " +
		"stderr and recent generation attempts live in the server process and are " +
		"not readable from disk. "

	if serverListening(host, port) {
		return base + fmt.Sprintf(
			"A server IS listening on port %d, but this command deliberately does not "+
				"authenticate to it. To include that section, open the chat, click the "+
				"status label in the header, and copy the report from there.", port)
	}
	return base + "No gobbonet server is listening, so there was nothing to ask."
}

// serverListening dials the configured bind. A wildcard host is checked on
// loopback, which is where it would be reachable from here regardless.
func serverListening(host string, port int) bool {
	if port == 0 {
		return false
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	conn, err := net.DialTimeout("tcp",
		net.JoinHostPort(host, strconv.Itoa(port)), 750*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
