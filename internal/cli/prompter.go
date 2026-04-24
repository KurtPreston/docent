package cli

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

type Prompter interface {
	Confirm(prompt string, defaultValue bool) (bool, error)
	Ask(prompt, defaultValue string) (string, error)
}

type StdioPrompter struct {
	In  io.Reader
	Out io.Writer
}

func (p StdioPrompter) Confirm(prompt string, defaultValue bool) (bool, error) {
	suffix := "y/N"
	if defaultValue {
		suffix = "Y/n"
	}
	answer, err := p.Ask(fmt.Sprintf("%s [%s]", prompt, suffix), "")
	if err != nil {
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer == "" {
		return defaultValue, nil
	}
	return answer == "y" || answer == "yes", nil
}

func (p StdioPrompter) Ask(prompt, defaultValue string) (string, error) {
	out := p.Out
	if out == nil {
		out = io.Discard
	}
	in := p.In
	if in == nil {
		in = strings.NewReader("")
	}
	if defaultValue != "" {
		fmt.Fprintf(out, "%s [%s]: ", prompt, defaultValue)
	} else {
		fmt.Fprintf(out, "%s: ", prompt)
	}
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", err
		}
		return defaultValue, nil
	}
	value := strings.TrimSpace(scanner.Text())
	if value == "" {
		return defaultValue, nil
	}
	return value, nil
}
