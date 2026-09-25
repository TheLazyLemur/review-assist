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

	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

func runInit(path string) error {
	example := filepath.Join(filepath.Dir(path), "config.example.json")
	if err := writeExampleConfig(example); err != nil {
		return err
	}
	fmt.Printf("Wrote %s: every option, for reference.\n", example)
	if _, err := os.Stat(path); err == nil {
		fmt.Printf("%s already exists. It is unchanged.\n", path)
		return nil
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

func writeInitConfig(path, token string) error {
	if token == "" {
		panic("writeInitConfig: empty token")
	}
	// Only the keys init sets. The test loads the result back through fileConfig.
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

// writeExampleConfig always overwrites: the example is documentation and must
// match this version. The token is left empty on purpose.
func writeExampleConfig(path string) error {
	var fc fileConfig
	fc.Backend = ptr(backendClaudeCode)
	fc.MessagesAPI.BaseURL = ptr("http://localhost:11434")
	fc.MessagesAPI.APIKey = ptr("ollama")
	fc.MessagesAPI.Model = ptr(defaultMessagesModel)
	fc.MessagesAPI.Effort = ptr(review.EffortNone)
	fc.MessagesAPI.Log = ptr("")
	fc.ClaudeCode.Token = ptr("")
	fc.ClaudeCode.Model = ptr("sonnet")
	fc.ClaudeCode.Effort = ptr(review.EffortMedium)
	fc.ClaudeCode.Executable = ptr("claude")
	fc.Review.Concurrency = ptr(4)
	fc.Review.MaxTurns = ptr(40)
	data, err := json.MarshalIndent(fc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write example config: %w", err)
	}
	return nil
}

func ptr[T any](v T) *T { return &v }
