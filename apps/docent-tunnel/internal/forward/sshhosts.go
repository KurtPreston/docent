package forward

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// SSHHost is one concrete (non-pattern) Host alias from an ssh_config file.
type SSHHost struct {
	Alias    string
	HostName string // literal HostName from the config, when set
}

// ListSSHHosts returns the concrete Host aliases in an ssh_config file and the
// files it Includes, in order, for pickers. Wildcard/negated patterns and Match
// blocks are skipped; HostName is the literal value (no token expansion).
func ListSSHHosts(path string) ([]SSHHost, error) {
	l := hostLister{seen: map[string]int{}}
	if err := l.scan(path, 0); err != nil {
		return nil, err
	}
	return l.hosts, nil
}

type hostLister struct {
	hosts []SSHHost
	seen  map[string]int // alias -> index in hosts
}

// scan walks one config file, recursing into Include directives.
func (l *hostLister) scan(path string, depth int) error {
	f, err := os.Open(path)
	if err != nil {
		if depth > 0 && os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	// current holds the indices of the aliases the open Host block applies to.
	var current []int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, args := splitConfigLine(sc.Text())
		switch key {
		case "host":
			current = current[:0]
			for _, a := range args {
				if strings.ContainsAny(a, "*?!") {
					continue
				}
				current = append(current, l.add(a))
			}
		case "match":
			current = current[:0]
		case "hostname":
			// First HostName wins, matching OpenSSH.
			for _, i := range current {
				if l.hosts[i].HostName == "" && len(args) > 0 {
					l.hosts[i].HostName = args[0]
				}
			}
		case "include":
			if depth >= 16 {
				continue
			}
			for _, inc := range includeFiles(args) {
				_ = l.scan(inc, depth+1)
			}
		}
	}
	return sc.Err()
}

// add records an alias once and returns its index.
func (l *hostLister) add(alias string) int {
	if i, ok := l.seen[alias]; ok {
		return i
	}
	l.hosts = append(l.hosts, SSHHost{Alias: alias})
	l.seen[alias] = len(l.hosts) - 1
	return len(l.hosts) - 1
}

// includeFiles expands Include arguments: ~ expansion, paths relative to
// ~/.ssh (as for a user config), and glob patterns in sorted order.
func includeFiles(args []string) []string {
	var files []string
	for _, a := range args {
		p := expandUser(a)
		if !filepath.IsAbs(p) {
			if home, err := os.UserHomeDir(); err == nil {
				p = filepath.Join(home, ".ssh", p)
			}
		}
		matches, _ := filepath.Glob(p)
		sort.Strings(matches)
		files = append(files, matches...)
	}
	return files
}

// splitConfigLine splits an ssh_config line into a lowercase keyword and its
// arguments. Keywords may be separated by whitespace or "="; arguments may be
// double-quoted. Blank lines and comments return an empty key.
func splitConfigLine(line string) (string, []string) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", nil
	}

	// The keyword ends at the first space or "=".
	end := strings.IndexFunc(line, func(r rune) bool { return unicode.IsSpace(r) || r == '=' })
	if end < 0 {
		return strings.ToLower(line), nil
	}
	key := strings.ToLower(line[:end])
	rest := strings.TrimLeftFunc(line[end:], unicode.IsSpace)
	rest = strings.TrimLeftFunc(strings.TrimPrefix(rest, "="), unicode.IsSpace)

	// Tokenize the remainder, honoring double quotes.
	var args []string
	var tok strings.Builder
	inQuote, hasTok := false, false
	for _, r := range rest {
		switch {
		case r == '"':
			inQuote, hasTok = !inQuote, true
		case unicode.IsSpace(r) && !inQuote:
			if hasTok {
				args = append(args, tok.String())
				tok.Reset()
				hasTok = false
			}
		default:
			tok.WriteRune(r)
			hasTok = true
		}
	}
	if hasTok {
		args = append(args, tok.String())
	}
	return key, args
}
