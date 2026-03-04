package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Prompter prints a message and reads a line of user input.
type Prompter interface {
	Ask(prompt string) (string, error)
}

// stdinPrompter reads from os.Stdin.
type stdinPrompter struct {
	scanner *bufio.Scanner
}

func newStdinPrompter() *stdinPrompter {
	return &stdinPrompter{scanner: bufio.NewScanner(os.Stdin)}
}

func (p *stdinPrompter) Ask(prompt string) (string, error) {
	for {
		fmt.Print(prompt)
		if !p.scanner.Scan() {
			if err := p.scanner.Err(); err != nil {
				return "", err
			}
			return "", fmt.Errorf("EOF")
		}
		line := strings.TrimSpace(p.scanner.Text())
		if !hasModifierArtifacts(line) {
			return line, nil
		}
	}
}

// hasModifierArtifacts reports whether s contains characters that indicate a
// modifier key (Ctrl, Alt, Win) was held when the input was produced.
// Control characters (ASCII < 32) come from Ctrl+key; ESC prefix (0x1B) is
// the lead byte of Alt+key sequences on most terminals.
func hasModifierArtifacts(s string) bool {
	for _, r := range s {
		if r < 32 || r == 127 {
			return true
		}
	}
	return false
}
