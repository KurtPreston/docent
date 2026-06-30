package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/AlecAivazis/survey/v2"
)

type Prompter interface {
	Confirm(prompt string, defaultValue bool) (bool, error)
	Ask(prompt, defaultValue string) (string, error)
	Select(prompt string, options []string, defaultValue string) (string, error)
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

// Select renders an arrow-key menu via survey.AskOne. defaultValue, when
// non-empty and present in options, is highlighted as the starting choice.
// StdioPrompter is only constructed in interactive contexts (see
// App.stdinIsTerminal), so survey is wired to the real os.Stdin/Stdout/
// Stderr triple just like the mode picker in App.Run.
func (p StdioPrompter) Select(prompt string, options []string, defaultValue string) (string, error) {
	if len(options) == 0 {
		return "", fmt.Errorf("Select: no options provided")
	}
	sel := &survey.Select{
		Message: prompt,
		Options: options,
	}
	if defaultValue != "" {
		sel.Default = defaultValue
	}
	var pick string
	if err := survey.AskOne(sel, &pick, survey.WithStdio(os.Stdin, os.Stdout, os.Stderr)); err != nil {
		return "", err
	}
	return pick, nil
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
