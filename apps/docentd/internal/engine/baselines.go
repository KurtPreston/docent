package engine

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/url"
	"os"
	"path/filepath"

	"github.com/KurtPreston/docent/libs/config/docentconfig"
	"github.com/KurtPreston/docent/libs/model"
)

// Transition baselines are each state unit's last entity snapshot, kept on
// disk so they outlive the daemon. Without them the first collect after a
// restart could only seed a fresh baseline, and any change made while docentd
// was down, such as a push to a watched PR, would never fire its rule.

// baselinesDir is the subdirectory of docent's state directory that holds one
// JSON snapshot per state unit.
const baselinesDir = "automation-baselines"

// baselineStore reads and writes per-unit entity snapshots under dir. A nil
// store, or one with an empty dir, keeps nothing.
type baselineStore struct {
	dir string
}

// defaultBaselineStore keeps snapshots under docent's state directory.
func defaultBaselineStore() *baselineStore {
	return &baselineStore{dir: filepath.Join(docentconfig.StateDir(), baselinesDir)}
}

// path returns the snapshot file for a unit key such as "github/state". The
// key is escaped so a directive ID can't reach outside dir.
func (s *baselineStore) path(snapKey string) string {
	return filepath.Join(s.dir, url.PathEscape(snapKey)+".json")
}

// load returns the unit's saved snapshot, or nil when there is none or it
// can't be read. An unreadable snapshot is logged and treated as missing, so
// the unit falls back to seeding a fresh baseline.
func (s *baselineStore) load(snapKey string) map[string]model.Entity {
	if s == nil || s.dir == "" {
		return nil
	}
	data, err := os.ReadFile(s.path(snapKey))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("docentd: read transition baseline %s: %v", snapKey, err)
		}
		return nil
	}

	// Decode, ignoring a corrupt file rather than failing the collect
	var ents map[string]model.Entity
	if err := json.Unmarshal(data, &ents); err != nil {
		log.Printf("docentd: ignoring corrupt transition baseline %s: %v", snapKey, err)
		return nil
	}
	return ents
}

// save writes the unit's snapshot atomically, through a temporary file and a
// rename, so a crash mid-write leaves the previous snapshot intact.
func (s *baselineStore) save(snapKey string, ents map[string]model.Entity) error {
	if s == nil || s.dir == "" {
		return nil
	}
	data, err := json.Marshal(ents)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}

	// Write beside the target, then swap it in
	tmp, err := os.CreateTemp(s.dir, ".baseline-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path(snapKey))
}
