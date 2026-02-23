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
	fmt.Print(prompt)
	if !p.scanner.Scan() {
		if err := p.scanner.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("EOF")
	}
	return strings.TrimSpace(p.scanner.Text()), nil
}
