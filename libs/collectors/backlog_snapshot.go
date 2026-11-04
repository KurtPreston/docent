package collectors

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type backlogSnapshotTask struct {
	ID       string `yaml:"id"`
	Title    string `yaml:"title"`
	Status   string `yaml:"status"`
	Sections map[string]string
}

type backlogTreeFile struct {
	hash, mode string
}

func (h *backlogHistory) snapshot(ctx context.Context, revision string) (map[string]backlogSnapshotTask, error) {
	out, err := h.git(ctx, []string{"ls-tree", "-r", "-z", "--full-tree", revision, "--", h.prefix + "."}, "")
	if err != nil {
		return nil, err
	}
	files := map[string]backlogTreeFile{}
	for _, entry := range bytes.Split(out, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		parts := bytes.SplitN(entry, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("backlog history: malformed tree entry")
		}
		meta := strings.Fields(string(parts[0]))
		if len(meta) != 3 {
			return nil, fmt.Errorf("backlog history: malformed tree metadata")
		}
		name := strings.TrimPrefix(string(parts[1]), h.prefix)
		files[name] = backlogTreeFile{hash: meta[2], mode: meta[0]}
	}
	dir, err := h.directory(ctx, files)
	if err != nil {
		return nil, fmt.Errorf("backlog history %s: %w", revision, err)
	}
	var wanted []string
	for name, file := range files {
		if backlogTaskPath(name, dir) {
			if file.mode != "100644" && file.mode != "100755" {
				return nil, fmt.Errorf("backlog history: task %s is not a regular file", name)
			}
			if _, parsed := h.tasks[file.hash]; !parsed {
				wanted = append(wanted, file.hash)
			}
		}
	}
	if err := h.loadBlobs(ctx, wanted); err != nil {
		return nil, err
	}

	// Parse each task blob once per run; IDs are validated per snapshot.
	tasks := map[string]backlogSnapshotTask{}
	for name, file := range files {
		if !backlogTaskPath(name, dir) {
			continue
		}
		task, parsed := h.tasks[file.hash]
		if !parsed {
			task, err = parseBacklogSnapshot(h.blobs[file.hash])
			if err != nil {
				return nil, fmt.Errorf("backlog history %s file %s: %w", revision, name, err)
			}
			h.parses++
			h.tasks[file.hash] = task
			// The parsed form replaces the raw bytes for the rest of the run.
			delete(h.blobs, file.hash)
		}
		if _, duplicate := tasks[task.ID]; duplicate {
			return nil, fmt.Errorf("backlog history %s: duplicate task ID %s", revision, task.ID)
		}
		tasks[task.ID] = task
	}
	h.snapshots++
	return tasks, nil
}

// Resolve the layout at each revision, including historical directory moves.
func (h *backlogHistory) directory(ctx context.Context, files map[string]backlogTreeFile) (string, error) {
	if root, ok := files["backlog.config.yml"]; ok {
		if err := h.loadBlobs(ctx, []string{root.hash}); err != nil {
			return "", err
		}
		var config struct {
			Directory      string `yaml:"backlog_directory"`
			DirectoryCamel string `yaml:"backlogDirectory"`
		}
		if err := yaml.Unmarshal(h.blobs[root.hash], &config); err != nil {
			return "", fmt.Errorf("invalid backlog.config.yml: %w", err)
		}
		dir := config.Directory
		if dir == "" {
			dir = config.DirectoryCamel
		}
		if dir != "" {
			dir = path.Clean(strings.ReplaceAll(strings.TrimSpace(dir), "\\", "/"))
			if dir == "." || dir == ".." || strings.HasPrefix(dir, "../") || strings.HasPrefix(dir, "/") || strings.Contains(dir, ":") {
				return "", fmt.Errorf("invalid backlog_directory %q", dir)
			}
			return dir, nil
		}
	}
	for _, dir := range []string{"backlog", ".backlog"} {
		for _, config := range []string{"config.yml", "config.yaml"} {
			if _, ok := files[dir+"/"+config]; ok {
				return dir, nil
			}
		}
	}
	for _, dir := range []string{"backlog", ".backlog"} {
		for name := range files {
			if strings.HasPrefix(name, dir+"/") {
				return dir, nil
			}
		}
	}
	return "", nil
}

func backlogTaskPath(name, dir string) bool {
	if dir == "" || !strings.HasSuffix(strings.ToLower(name), ".md") {
		return false
	}
	parent := path.Dir(name)
	return parent == dir+"/tasks" || parent == dir+"/archive/tasks" || parent == dir+"/completed"
}

// Read all uncached blobs in one subprocess. Size framing handles arbitrary
// Markdown contents (including text that resembles Git's batch headers).
func (h *backlogHistory) loadBlobs(ctx context.Context, hashes []string) error {
	seen := map[string]bool{}
	var missing []string
	for _, hash := range hashes {
		if _, ok := h.blobs[hash]; !ok && !seen[hash] {
			seen[hash] = true
			missing = append(missing, hash)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	out, err := h.git(ctx, []string{"cat-file", "--batch"}, strings.Join(missing, "\n")+"\n")
	if err != nil {
		return err
	}
	for _, hash := range missing {
		end := bytes.IndexByte(out, '\n')
		if end < 0 {
			return fmt.Errorf("backlog history: missing blob header for %s", hash)
		}
		header := strings.Fields(string(out[:end]))
		if len(header) != 3 || header[0] != hash || header[1] != "blob" {
			return fmt.Errorf("backlog history: unavailable blob %s", hash)
		}
		size, err := strconv.Atoi(header[2])
		out = out[end+1:]
		if err != nil || size < 0 || size >= len(out) || out[size] != '\n' {
			return fmt.Errorf("backlog history: invalid blob size for %s", hash)
		}
		h.blobs[hash] = bytes.Clone(out[:size])
		out = out[size+1:]
	}
	return nil
}

func parseBacklogSnapshot(content []byte) (backlogSnapshotTask, error) {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	var task backlogSnapshotTask
	if !strings.HasPrefix(text, "---\n") {
		return task, fmt.Errorf("missing task frontmatter")
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return task, fmt.Errorf("unterminated task frontmatter")
	}
	end += 4
	if len(text) > end+4 && text[end+4] != '\n' {
		return task, fmt.Errorf("invalid task frontmatter delimiter")
	}
	if err := yaml.Unmarshal([]byte(text[4:end]), &task); err != nil {
		return task, err
	}
	task.ID = strings.ToUpper(strings.TrimSpace(task.ID))
	if task.ID == "" || strings.TrimSpace(task.Title) == "" || strings.TrimSpace(task.Status) == "" {
		return task, fmt.Errorf("task id, title, and status are required")
	}
	task.Sections = backlogSections(text[end+4:])
	return task, nil
}

func backlogSections(body string) map[string]string {
	sections := map[string]string{}
	section := "content"
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "## ") {
			section = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "## ")))
			continue
		}
		if line == "" || (strings.HasPrefix(line, "<!--") && strings.HasSuffix(line, "-->")) {
			continue
		}
		sections[section] += strings.Join(strings.Fields(line), " ") + "\n"
	}
	for key, value := range sections {
		sections[key] = strings.TrimSpace(value)
	}
	return sections
}
