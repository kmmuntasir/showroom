// Package upload implements democtl's zip deployment pipeline
// (docs/demos.md §Upload pipeline): stream the upload into tmp/, treat
// the archive as hostile input, extract it into
// releases/<demoID>-<unix>/, verify the root index.html, then atomically
// swap demos/<name>/current — a deploy is a symlink rename, never a
// half-written tree (design decision D6).
package upload

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"democtl/internal/config"
)

// Sentinel errors call sites branch on (the handler maps them to 400s);
// everything else is a wrapped filesystem or zip error.
var (
	ErrZipTooLarge   = errors.New("upload: zip exceeds the size limit")
	ErrTotalTooLarge = errors.New("upload: extracted total exceeds the size limit")
	ErrFileTooLarge  = errors.New("upload: single file exceeds the size limit")
	ErrTooManyFiles  = errors.New("upload: too many files in the archive")
	ErrBadEntry      = errors.New("upload: unsafe archive entry")
	ErrNoIndexHTML   = errors.New("upload: archive has no index.html at its root")
	ErrNotAZip       = errors.New("upload: not a zip archive")
)

// errNotPlain is returned when an argument spliced into a path is not a
// single plain segment. demonames and the store own the public name
// policy, so this should be unreachable from handlers — it is the local
// traversal guard for this package.
var errNotPlain = errors.New("upload: not a plain path segment")

const (
	demosDir    = "demos"
	releasesDir = "releases"
	tmpDir      = "tmp"
	currentName = "current"
	indexHTML   = "index.html"
)

// Store is the on-disk demo store. Layout under DataDir (created on
// demand, docs/demos.md §Components):
//
//	DataDir/demos/<name>/current    relative symlink -> ../../releases/<dir>
//	DataDir/releases/<dir>/         extracted tree; <dir> = "<demoID>-<unixSeconds>"
//	DataDir/tmp/                    staging area for in-flight uploads
type Store struct {
	DataDir string
	Limits  config.Limits
}

// Result reports a successful publish. Dir is the basename under
// DataDir/releases/ — the same value store.Release.Dir carries.
type Result struct {
	Dir       string
	SizeBytes int64
	FileCount int64
}

// Publish runs the full pipeline (docs/demos.md §Upload pipeline):
// stream r into a temp file under tmp/, validate and extract it into a
// fresh releases/<demoID>-<unix>/, verify the root index.html, then
// atomically swap demos/<name>/current onto it. Any failure before the
// swap removes the partial release tree and the temp zip — failed
// deploys leave no debris.
func (s *Store) Publish(demoID int64, name string, r io.Reader) (Result, error) {
	if err := safeBase(name); err != nil {
		return Result{}, fmt.Errorf("upload: demo name %q: %w", name, err)
	}
	if err := s.ensureLayout(); err != nil {
		return Result{}, err
	}

	// 1. Stream the upload into a random-named temp file, counting bytes
	// so the compressed cap is enforced (docs/demos.md §Upload pipeline).
	tmpZip, err := os.CreateTemp(filepath.Join(s.DataDir, tmpDir), "*.zip")
	if err != nil {
		return Result{}, fmt.Errorf("upload: create temp zip: %w", err)
	}
	tmpPath := tmpZip.Name()
	// Removed on every path — success, validation failure, or extraction
	// failure; a stale Remove of a non-existent file is a harmless no-op.
	defer os.Remove(tmpPath)

	cw := &countingWriter{w: tmpZip}
	if _, err := io.Copy(cw, r); err != nil {
		tmpZip.Close()
		return Result{}, fmt.Errorf("upload: read upload: %w", err)
	}
	if err := tmpZip.Close(); err != nil {
		return Result{}, fmt.Errorf("upload: close temp zip: %w", err)
	}
	if cw.n > s.Limits.MaxZipBytes {
		return Result{}, fmt.Errorf("upload: %d bytes: %w", cw.n, ErrZipTooLarge)
	}

	dirName, err := s.createReleaseDir(demoID)
	if err != nil {
		return Result{}, err
	}
	releaseDir := filepath.Join(s.DataDir, releasesDir, dirName)

	res, err := s.extract(tmpPath, releaseDir)
	if err != nil {
		// Failed deploys leave no debris: drop the partial release tree
		// (the temp zip goes via the defer above).
		os.RemoveAll(releaseDir)
		return Result{}, err
	}

	// 6. Atomic switch: rename(2) over the symlink, so a static-file
	// reader never observes a missing or half-updated current (D6).
	if err := s.setCurrent(name, dirName); err != nil {
		return Result{}, fmt.Errorf("upload: activate %s: %w", dirName, err)
	}

	res.Dir = dirName
	return res, nil
}

// SwitchCurrent points demos/<name>/current at releases/<dir> — the
// rollback path (docs/demos.md HTTP surface, POST /rollback). dir must
// be a plain basename naming an existing release directory.
func (s *Store) SwitchCurrent(name, dir string) error {
	if err := safeBase(name); err != nil {
		return fmt.Errorf("upload: demo name %q: %w", name, err)
	}
	if err := safeBase(dir); err != nil {
		return fmt.Errorf("upload: release dir %q: %w", dir, err)
	}
	info, err := os.Stat(filepath.Join(s.DataDir, releasesDir, dir))
	if err != nil {
		return fmt.Errorf("upload: release %q: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("upload: release %q is not a directory", dir)
	}
	if err := s.setCurrent(name, dir); err != nil {
		return fmt.Errorf("upload: activate %s: %w", dir, err)
	}
	return nil
}

// RenameDemoDir renames demos/<oldName> to demos/<newName> — the file
// side of the subdomain-label rename (docs/demos.md HTTP surface, PATCH
// /api/demos/{name}). The relative current symlink keeps working under
// the new parent name, so no rewiring is needed. A demo that was never
// deployed has no dir; that is a no-op, not an error.
func (s *Store) RenameDemoDir(oldName, newName string) error {
	if err := safeBase(oldName); err != nil {
		return fmt.Errorf("upload: old demo name %q: %w", oldName, err)
	}
	if err := safeBase(newName); err != nil {
		return fmt.Errorf("upload: new demo name %q: %w", newName, err)
	}
	oldDir := filepath.Join(s.DataDir, demosDir, oldName)
	if _, err := os.Stat(oldDir); err != nil {
		if os.IsNotExist(err) {
			return nil // never deployed — nothing to rename
		}
		return fmt.Errorf("upload: stat demo dir %s: %w", oldName, err)
	}
	if err := os.Rename(oldDir, filepath.Join(s.DataDir, demosDir, newName)); err != nil {
		return fmt.Errorf("upload: rename demo %s: %w", oldName, err)
	}
	return nil
}

// RemoveReleaseDir deletes one release directory. dir must be a plain
// basename — separators and ".." are rejected before touching the
// filesystem.
func (s *Store) RemoveReleaseDir(dir string) error {
	if err := safeBase(dir); err != nil {
		return fmt.Errorf("upload: release dir %q: %w", dir, err)
	}
	if err := os.RemoveAll(filepath.Join(s.DataDir, releasesDir, dir)); err != nil {
		return fmt.Errorf("upload: remove release %s: %w", dir, err)
	}
	return nil
}

// RemoveDemoDirs removes demos/<name> and every releases/<demoID>-*
// release — the file side of demo deletion (docs/demos.md HTTP surface,
// DELETE /api/demos/{name}). The demoID prefix cannot match another
// demo's releases: "1-" never prefixes "11-…".
func (s *Store) RemoveDemoDirs(demoID int64, name string) error {
	if err := safeBase(name); err != nil {
		return fmt.Errorf("upload: demo name %q: %w", name, err)
	}
	if err := os.RemoveAll(filepath.Join(s.DataDir, demosDir, name)); err != nil {
		return fmt.Errorf("upload: remove demo dir %s: %w", name, err)
	}
	prefix := fmt.Sprintf("%d-", demoID)
	entries, err := os.ReadDir(filepath.Join(s.DataDir, releasesDir))
	if err != nil {
		return fmt.Errorf("upload: list releases: %w", err)
	}
	var errs []error
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.DataDir, releasesDir, e.Name())); err != nil {
			errs = append(errs, fmt.Errorf("upload: remove release %s: %w", e.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// CleanTmp wipes the contents of tmp/ — the staging area holds nothing
// but in-flight uploads and is wiped at boot (docs/demos.md §Security
// posture). The directory itself stays.
func (s *Store) CleanTmp() error {
	tmp := filepath.Join(s.DataDir, tmpDir)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return fmt.Errorf("upload: create %s dir: %w", tmpDir, err)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return fmt.Errorf("upload: list %s: %w", tmpDir, err)
	}
	var errs []error
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(tmp, e.Name())); err != nil {
			errs = append(errs, fmt.Errorf("upload: clean %s: %w", e.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// extract validates every entry of the archive at zipPath — headers
// only, before a single byte is decompressed — and then extracts the
// regular files into releaseDir. The archive is untrusted input: entry
// names, entry types, and all three size/count ceilings are enforced
// twice, once from the headers and once against the bytes that actually
// move, so a lying header cannot bypass a cap (docs/demos.md §Upload
// pipeline).
func (s *Store) extract(zipPath, releaseDir string) (Result, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return Result{}, fmt.Errorf("upload: open archive: %w (%v)", ErrNotAZip, err)
	}
	defer zr.Close()

	entries, err := s.scan(zr)
	if err != nil {
		return Result{}, err
	}

	// Prefix tolerance — a deliberate leniency (docs/demos.md §Upload
	// pipeline step 3): PMs keep zipping the dist/ folder itself, so when
	// there is no root index.html but every entry sits under one shared
	// top-level directory that contains index.html, strip that single
	// prefix for extraction instead of bouncing the upload.
	prefix := ""
	if !hasFile(entries, indexHTML) {
		if top, ok := singleTopDir(entries); ok && hasFile(entries, top+"/"+indexHTML) {
			prefix = top
		}
	}

	var res Result
	for _, e := range entries {
		name := e.clean
		if prefix != "" {
			// The top dir's own entry ("dist/") collapses onto the release
			// root; TrimPrefix alone would leave the bare name untouched.
			if name == prefix {
				name = ""
			} else {
				name = strings.TrimPrefix(name, prefix+"/")
			}
		}
		dest, err := safeJoin(releaseDir, name)
		if err != nil {
			return Result{}, fmt.Errorf("upload: entry %q: %w", e.raw, err)
		}
		if e.isDir {
			if name != "" {
				if err := os.MkdirAll(dest, 0o755); err != nil {
					return Result{}, fmt.Errorf("upload: mkdir %s: %w", dest, err)
				}
			}
			continue
		}
		if name == "" {
			// Unreachable after scan + singleTopDir; kept as the last
			// guard against a file collapsing onto the release dir.
			return Result{}, fmt.Errorf("upload: entry %q: %w", e.raw, ErrBadEntry)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return Result{}, fmt.Errorf("upload: mkdir %s: %w", filepath.Dir(dest), err)
		}
		n, err := s.copyEntry(e.file, dest)
		if err != nil {
			return Result{}, err
		}
		res.SizeBytes += n
		res.FileCount++
		if res.SizeBytes > s.Limits.MaxTotalBytes {
			return Result{}, fmt.Errorf("upload: %d bytes so far: %w", res.SizeBytes, ErrTotalTooLarge)
		}
	}

	// 5. It is a website or it is not: index.html must sit at the root of
	// the extracted release (docs/demos.md §Upload pipeline).
	if info, err := os.Stat(filepath.Join(releaseDir, indexHTML)); err != nil || info.IsDir() {
		return Result{}, fmt.Errorf("upload: %w", ErrNoIndexHTML)
	}
	return res, nil
}

// entry is one validated archive record. file is nil for directories.
type entry struct {
	raw   string // as written in the archive, for error messages
	clean string // cleaned relative slash path
	isDir bool
	file  *zip.File
}

// scan walks the archive in order, validating names and entry types and
// enforcing the count/size ceilings from the header sizes alone.
func (s *Store) scan(zr *zip.ReadCloser) ([]entry, error) {
	entries := make([]entry, 0, len(zr.File))
	var total int64
	files := 0
	for _, zf := range zr.File {
		clean, err := cleanEntryName(zf.Name)
		if err != nil {
			return nil, fmt.Errorf("upload: entry %q: %w", zf.Name, err)
		}
		mode := zf.Mode()
		isDir := mode.IsDir() || strings.HasSuffix(zf.Name, "/")
		switch {
		case mode&os.ModeSymlink != 0:
			// Zip pseudo-symlinks are rejected outright — a published
			// demo is plain files only (docs/demos.md §Upload pipeline).
			return nil, fmt.Errorf("upload: entry %q: %w", zf.Name, ErrBadEntry)
		case isDir:
			// Tracked so empty dirs and the shared top dir are preserved;
			// never extracted as files.
			entries = append(entries, entry{raw: zf.Name, clean: clean, isDir: true})
		case !mode.IsRegular():
			return nil, fmt.Errorf("upload: entry %q: %w", zf.Name, ErrBadEntry)
		default:
			files++
			if files > s.Limits.MaxFiles {
				return nil, fmt.Errorf("upload: %d entries: %w", files, ErrTooManyFiles)
			}
			if zf.UncompressedSize64 > uint64(s.Limits.MaxFileBytes) {
				return nil, fmt.Errorf("upload: entry %q at %d bytes: %w",
					zf.Name, zf.UncompressedSize64, ErrFileTooLarge)
			}
			total += int64(zf.UncompressedSize64)
			if total > s.Limits.MaxTotalBytes {
				return nil, fmt.Errorf("upload: %d bytes across the archive: %w", total, ErrTotalTooLarge)
			}
			entries = append(entries, entry{raw: zf.Name, clean: clean, file: zf})
		}
	}
	return entries, nil
}

// copyEntry decompresses one regular file into dest. The header size was
// already checked in scan; the LimitReader enforces the same cap against
// the bytes that actually arrive, so a forged header cannot smuggle more
// through (docs/demos.md §Upload pipeline — zip-bomb guard).
func (s *Store) copyEntry(zf *zip.File, dest string) (int64, error) {
	rc, err := zf.Open()
	if err != nil {
		return 0, fmt.Errorf("upload: read entry %q: %w (%v)", zf.Name, ErrNotAZip, err)
	}
	defer rc.Close()

	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, fmt.Errorf("upload: create %s: %w", dest, err)
	}
	n, err := io.Copy(f, io.LimitReader(rc, s.Limits.MaxFileBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, fmt.Errorf("upload: extract %q: %w", zf.Name, err)
	}
	if n > s.Limits.MaxFileBytes {
		return n, fmt.Errorf("upload: entry %q wrote %d bytes: %w", zf.Name, n, ErrFileTooLarge)
	}
	return n, nil
}

// setCurrent repoints demos/<name>/current at releases/<dir>. The new
// symlink is built under a temp name and renamed over current — rename
// over an existing symlink is atomic, so the static host never observes
// a missing or half-updated state (docs/demos.md design decision D6).
func (s *Store) setCurrent(name, dir string) error {
	demoDir := filepath.Join(s.DataDir, demosDir, name)
	if err := os.MkdirAll(demoDir, 0o755); err != nil {
		return fmt.Errorf("upload: create %s: %w", demoDir, err)
	}
	// CreateTemp hands out a collision-free name; the placeholder file is
	// replaced by the symlink at the same path.
	f, err := os.CreateTemp(demoDir, ".current-tmp-*")
	if err != nil {
		return fmt.Errorf("upload: temp symlink: %w", err)
	}
	tmp := f.Name()
	f.Close()
	os.Remove(tmp)

	target := filepath.Join("..", "..", releasesDir, dir)
	if err := os.Symlink(target, tmp); err != nil {
		return fmt.Errorf("upload: symlink %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, filepath.Join(demoDir, currentName)); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("upload: swap current: %w", err)
	}
	return nil
}

// createReleaseDir makes an empty releases/<demoID>-<unixSeconds> dir
// and returns its basename. On the rare same-second collision a -N
// suffix is appended — two rapid deploys must never merge into one
// release tree, or stale files of the first would survive in the second.
func (s *Store) createReleaseDir(demoID int64) (string, error) {
	base := fmt.Sprintf("%d-%d", demoID, time.Now().Unix())
	for i := 0; i < 100; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		switch err := os.Mkdir(filepath.Join(s.DataDir, releasesDir, name), 0o755); {
		case err == nil:
			return name, nil
		case !os.IsExist(err):
			return "", fmt.Errorf("upload: create release %s: %w", name, err)
		}
	}
	return "", fmt.Errorf("upload: release %s: %w", base, os.ErrExist)
}

// ensureLayout creates the DataDir skeleton on demand (docs/demos.md
// §Components: demos/, releases/, tmp/ under DEMOCTL_DATA_DIR).
func (s *Store) ensureLayout() error {
	for _, d := range []string{demosDir, releasesDir, tmpDir} {
		if err := os.MkdirAll(filepath.Join(s.DataDir, d), 0o755); err != nil {
			return fmt.Errorf("upload: create %s dir: %w", d, err)
		}
	}
	return nil
}

// cleanEntryName validates one archive entry name and returns its
// cleaned slash path: no backslash, no leading slash, no ".." segment,
// never empty (docs/demos.md §Upload pipeline — entry name rules).
func cleanEntryName(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, '\\') || strings.HasPrefix(name, "/") {
		return "", ErrBadEntry
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return "", ErrBadEntry
		}
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", ErrBadEntry
	}
	return clean, nil
}

// safeJoin joins rel (a cleaned slash path) under root and proves the
// result stays inside it — the last line of defense behind
// cleanEntryName's traversal rejection.
func safeJoin(root, rel string) (string, error) {
	dest := filepath.Join(root, filepath.FromSlash(rel))
	inside, err := filepath.Rel(root, dest)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", ErrBadEntry
	}
	return dest, nil
}

// safeBase accepts only a single plain path segment. Demo names and
// release dir basenames are spliced into paths throughout this package;
// callers validate the public policy, this is the local traversal guard.
func safeBase(p string) error {
	switch {
	case p == "", p == ".", p == "..":
		return fmt.Errorf("%q: %w", p, errNotPlain)
	case strings.ContainsRune(p, '/'),
		strings.ContainsRune(p, '\\'),
		strings.ContainsRune(p, filepath.Separator),
		filepath.VolumeName(p) != "":
		return fmt.Errorf("%q: %w", p, errNotPlain)
	}
	return nil
}

// singleTopDir reports the one shared top-level directory every entry
// lives under. The top dir's own entry ("dist/") may clean to the bare
// name; a regular file at that name means the archive has no single
// clean root and gets no leniency.
func singleTopDir(entries []entry) (string, bool) {
	if len(entries) == 0 {
		return "", false
	}
	top, _, _ := strings.Cut(entries[0].clean, "/")
	if top == "" {
		return "", false
	}
	for _, e := range entries {
		if e.clean == top && e.isDir {
			continue
		}
		if !strings.HasPrefix(e.clean, top+"/") {
			return "", false
		}
	}
	return top, true
}

// hasFile reports whether a regular-file entry with the given cleaned
// name is present.
func hasFile(entries []entry, clean string) bool {
	for _, e := range entries {
		if !e.isDir && e.clean == clean {
			return true
		}
	}
	return false
}

// countingWriter counts bytes as they stream past so the compressed
// upload cap can be enforced after the copy (docs/demos.md §Upload
// pipeline). The handler edge should also wrap r in http.MaxBytesReader.
type countingWriter struct {
	w io.Writer
	n int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.n += int64(n)
	return n, err
}
