package forward

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestListSSHHosts(t *testing.T) {
	dir := t.TempDir()
	inc := filepath.Join(dir, "conf.d")
	if err := os.Mkdir(inc, 0o700); err != nil {
		t.Fatal(err)
	}

	// The included file is listed at the point of its Include line.
	write := func(p, body string) {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(inc, "work"), "Host jump\n  HostName jump.example.com\n")
	main := filepath.Join(dir, "config")
	write(main, `# comment
Host workspace
    HostName main.ws.coder

Include `+inc+`/*

Host desktop laptop *.corp !bad
  HostName=desktop.lan
  User me

Host coder.*
  ProxyCommand coder ssh --stdio %h

Match host *.coder
  HostName ignored

Host "quoted host" workspace
  HostName second.wins.not
`)

	got, err := ListSSHHosts(main)
	if err != nil {
		t.Fatal(err)
	}
	want := []SSHHost{
		{Alias: "workspace", HostName: "main.ws.coder"},
		{Alias: "jump", HostName: "jump.example.com"},
		{Alias: "desktop", HostName: "desktop.lan"},
		{Alias: "laptop", HostName: "desktop.lan"},
		{Alias: "quoted host", HostName: "second.wins.not"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListSSHHosts:\n got %+v\nwant %+v", got, want)
	}
}

func TestListSSHHostsMissingFile(t *testing.T) {
	if _, err := ListSSHHosts(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected an error for a missing top-level config")
	}
}
