package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/term"
)

// runInit writes a first config file that uses the Claude Code backend. It
// asks for a token from `claude setup-token`; an empty answer runs that
// command first. It never overwrites an existing file.
func runInit(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists: edit it, or delete it and run --init again", path)
	}
	fmt.Printf("review-assist will write %s with the claude-code backend.\n\n", path)
	fmt.Println("Paste a token from `claude setup-token`, or press enter to run it now.")
	token, err := readSecret("Token: ")
	if err != nil {
		return err
	}
	if token == "" {
		fmt.Println("\nRunning `claude setup-token`. Copy the token it prints.")
		cmd := exec.Command("claude", "setup-token")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("claude setup-token: %w", err)
		}
		if token, err = readSecret("\nToken: "); err != nil {
			return err
		}
	}
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return errors.New("that is not a token: expected one word from `claude setup-token`")
	}
	if err := writeInitConfig(path, token); err != nil {
		return err
	}
	fmt.Printf("\nWrote %s (readable by you only).\nRun review-assist in a repository to start.\n", path)
	return nil
}

// readSecret reads one line without echo when stdin is a terminal.
func readSecret(prompt string) (string, error) {
	fmt.Print(prompt)
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		b, err := term.ReadPassword(fd)
		fmt.Println()
		return strings.TrimSpace(string(b)), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// writeInitConfig creates the config file for the claude-code backend. The
// directory is created 0700 and the file 0600, and an existing file is left
// alone.
func writeInitConfig(path, token string) error {
	if token == "" {
		panic("writeInitConfig: empty token")
	}
	// Only the keys init sets, so the file is short to edit by hand. The keys
	// match fileConfig; the test loads the result back through it.
	data, err := json.MarshalIndent(map[string]any{
		"backend":     backendClaudeCode,
		"claude_code": map[string]any{"token": token},
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("%s already exists: edit it, or delete it and run --init again", path)
	}
	if err != nil {
		return fmt.Errorf("create config: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("write config: %w", err)
	}
	return f.Close()
}
