package wmclient

import "testing"

func TestParseCursorTitle(t *testing.T) {
	tests := []struct {
		title    string
		wantLeaf string
		wantHost string
	}{
		{"my-feature [SSH: ubuntu] - Cursor", "my-feature", "ubuntu"},
		{"file.ts - my-feature [SSH: ubuntu] - Cursor", "my-feature", "ubuntu"},
		{"local-project - Cursor", "local-project", ""},
	}
	for _, tc := range tests {
		leaf, host := ParseCursorTitle(tc.title)
		if leaf != tc.wantLeaf || host != tc.wantHost {
			t.Errorf("ParseCursorTitle(%q) = (%q,%q), want (%q,%q)", tc.title, leaf, host, tc.wantLeaf, tc.wantHost)
		}
	}
}
