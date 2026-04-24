package cli

import (
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
	line, err := readLine(in)
	if err != nil {
		if err != io.EOF {
			return "", err
		}
		if line != "" {
			value := strings.TrimSpace(line)
			if value != "" {
				return value, nil
			}
		}
		return defaultValue, nil
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return defaultValue, nil
	}
	return value, nil
}

func readLine(in io.Reader) (string, error) {
	var buf strings.Builder
	var one [1]byte
	for {
		n, err := in.Read(one[:])
		if n > 0 {
			if one[0] == '\n' {
				return buf.String(), nil
			}
			buf.WriteByte(one[0])
		}
		if err != nil {
			return buf.String(), err
		}
	}
}
