package daemoncontrol

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var errEndpointUnknown = errors.New("health endpoint cannot be resolved")

func waitReady(log io.Writer, binary string) error {
	sys.sleep(startupGrace)
	var last error
	for i := 0; i < 50; i++ {
		d, err := Inspect(binary)
		if err == nil && d.Running {
			err = sys.probe(d)
			if errors.Is(err, errEndpointUnknown) {
				fmt.Fprintf(log, "%v; verified process identity and startup survival only\n", err)
				err = nil
			}
			if err == nil && sys.alive(d.PIDs[0]) {
				fmt.Fprintln(log, "docentd is "+d.Describe())
				return nil
			}
		} else if err == nil {
			err = fmt.Errorf("daemon exited right after starting or has not started")
		}
		last = err
		sys.sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("docentd did not become ready: %w", last)
}

func (s realSystem) probe(d Daemon) error {
	l := d.Launch
	if s.goos() == "darwin" && d.Manager == ManagerLaunchd {
		out, err := s.run("plutil", "-extract", "ProgramArguments", "json", "-o", "-", d.plist)
		if err != nil {
			return fmt.Errorf("%w: cannot read LaunchAgent arguments", errEndpointUnknown)
		}
		if err := json.Unmarshal([]byte(out), &l.Args); err != nil {
			return fmt.Errorf("invalid LaunchAgent arguments: %w", err)
		}
	}
	url, err := healthURL(l)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 250 * time.Millisecond, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	defer client.CloseIdleConnections()
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16))
	if err != nil || resp.StatusCode != http.StatusOK || string(body) != "ok" {
		return fmt.Errorf("daemon health check failed at %s (status %d)", url, resp.StatusCode)
	}
	return nil
}

// Resolve only launch metadata, without loading collectors or modifying the
// CLI's environment. Relative config paths belong to the daemon's cwd.
func healthURL(l Launch) (string, error) {
	fs := flag.NewFlagSet("docentd", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	config := fs.String("config", "", "")
	port := fs.Int("port", 0, "")
	host := fs.String("host", "", "")
	fs.String("web", "", "")
	if len(l.Args) == 0 {
		return "", errEndpointUnknown
	}
	if err := fs.Parse(l.Args[1:]); err != nil {
		return "", fmt.Errorf("%w: %v", errEndpointUnknown, err)
	}
	env := map[string]string{}
	for _, entry := range l.Env {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	path := *config
	if path == "" {
		path = env["DOCENT_CONFIG"]
	}
	if path == "" {
		dir := env["DOCENT_CONFIG_DIR"]
		if dir == "" {
			if env["HOME"] == "" {
				return "", errEndpointUnknown
			}
			dir = filepath.Join(env["HOME"], ".config", "docent")
		}
		path = filepath.Join(dir, "docentd.yaml")
	}
	if !filepath.IsAbs(path) {
		if l.Dir == "" {
			return "", errEndpointUnknown
		}
		path = filepath.Join(l.Dir, path)
	}
	cfg := struct {
		Port int    `yaml:"port"`
		Host string `yaml:"bindHost"`
	}{Port: 39787}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("read health configuration: %w", err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("parse health configuration: %w", err)
	}
	if *port > 0 {
		cfg.Port = *port
	}
	if *host != "" {
		cfg.Host = *host
	}
	switch cfg.Host {
	case "", "0.0.0.0":
		cfg.Host = "127.0.0.1"
	case "::":
		cfg.Host = "::1"
	}
	return "http://" + net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)) + "/health", nil
}
