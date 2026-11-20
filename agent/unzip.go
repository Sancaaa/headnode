package agent

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxUnpackBytes caps what one archive may unpack to, counting the sizes the
// archive says it holds. It is checked again while the bytes are written, so an
// archive that lies about its sizes (a "zip bomb") cannot fill the disk anyway.
// A variable so a test can lower it.
var maxUnpackBytes int64 = 16 << 30

var (
	errUnsafeArchive = errors.New("the archive is not safe to unpack (a path that leaves the folder, a link, or a special file)")
	errUnpackTooBig  = errors.New("the archive unpacks to more than the limit")
)

// extractZip unpacks the zip archive at zipPath into the existing folder dest.
// It is what receiving a whole folder (`fetch` with extract) ends with, and the
// archive comes from outside the agent, so it is treated as hostile:
//
//   - every path is checked before anything is written, and one that is absolute,
//     contains "..", or uses a backslash makes the whole archive refused;
//   - links and special files are refused, so a file cannot be planted through a
//     link that points outside;
//   - the number of entries and the unpacked size are limited, and the size is
//     enforced while writing, not only trusted from the headers;
//   - nothing is written inside dest until everything has been unpacked into a
//     staging folder, and the top-level items are only moved in if none of them
//     exists yet. So a failed or refused unpack leaves dest exactly as it was.
func extractZip(zipPath, dest string) error {
	zr, err := zip.OpenReader(zipPath)
	if errors.Is(err, zip.ErrInsecurePath) {
		// Newer Go versions can report a path that climbs out while opening. That is
		// the same answer as our own check below.
		if zr != nil {
			zr.Close()
		}
		return errUnsafeArchive
	}
	if err != nil {
		return fmt.Errorf("the file is not a valid zip archive: %w", err)
	}
	defer zr.Close()

	if len(zr.File) > walkLimit {
		return fmt.Errorf("the archive has more than %d entries, which is too many to unpack", walkLimit)
	}
	var declared uint64
	for _, f := range zr.File {
		if err := checkEntry(f); err != nil {
			return err
		}
		declared += f.UncompressedSize64
		if declared > uint64(maxUnpackBytes) {
			return errUnpackTooBig
		}
	}

	// The staging folder is inside dest: the final moves are renames on one
	// filesystem, and a path that cannot be written is found here, before the
	// slow part.
	staging, err := os.MkdirTemp(dest, ".headnode-unpack-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	type dirMode struct {
		path string
		perm fs.FileMode
	}
	var dirs []dirMode
	var written int64
	for _, f := range zr.File {
		target := filepath.Join(staging, filepath.FromSlash(strings.TrimSuffix(f.Name, "/")))
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			dirs = append(dirs, dirMode{target, f.Mode().Perm()})
			continue
		}
		// An archive does not always list a folder before the files in it.
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		n, err := unpackFile(f, target, maxUnpackBytes-written)
		if err != nil {
			return err
		}
		written += n
	}
	// Folder modes last, deepest first: a folder stored as read-only could not
	// be filled otherwise.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i].path) > len(dirs[j].path) })
	for _, d := range dirs {
		if err := os.Chmod(d.path, d.perm); err != nil {
			return err
		}
	}

	// Move the top-level items in. Check them all first: if any is in the way,
	// nothing is moved, and the person is told which.
	items, err := os.ReadDir(staging)
	if err != nil {
		return err
	}
	for _, item := range items {
		if _, err := os.Lstat(filepath.Join(dest, item.Name())); err == nil {
			return fmt.Errorf("%q already exists in the destination folder", item.Name())
		}
	}
	for _, item := range items {
		if err := os.Rename(filepath.Join(staging, item.Name()), filepath.Join(dest, item.Name())); err != nil {
			return err
		}
	}
	return nil
}

// checkEntry refuses an archive entry that could write outside the folder it is
// unpacked into, or that is not a plain file or folder.
func checkEntry(f *zip.File) error {
	name := f.Name
	if strings.ContainsAny(name, "\\\x00") {
		return errUnsafeArchive
	}
	// IsLocal says: not absolute, no ".." that climbs out, not empty.
	if !filepath.IsLocal(filepath.FromSlash(strings.TrimSuffix(name, "/"))) {
		return errUnsafeArchive
	}
	mode := f.Mode()
	if mode&fs.ModeSymlink != 0 || (!mode.IsRegular() && !f.FileInfo().IsDir()) {
		return errUnsafeArchive
	}
	return nil
}

// unpackFile writes one archive entry to target, which must not exist. It
// writes at most limit bytes and never more than the entry said it holds.
func unpackFile(f *zip.File, target string, limit int64) (int64, error) {
	in, err := f.Open()
	if err != nil {
		return 0, err
	}
	defer in.Close()

	// O_EXCL: a second entry with the same name is an error, not a silent overwrite.
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	allowed := min(int64(f.UncompressedSize64), limit)
	n, err := io.Copy(out, io.LimitReader(in, allowed+1))
	if err != nil {
		out.Close()
		return n, err
	}
	if n > allowed {
		out.Close()
		return n, errUnpackTooBig // more bytes than the entry said, or than is left of the limit
	}
	if err := out.Close(); err != nil {
		return n, err
	}
	// Chmod, not the mode given to OpenFile: the process umask would have taken
	// bits away from that. Only the nine rwx bits: nothing arrives setuid.
	if err := os.Chmod(target, f.Mode().Perm()); err != nil {
		return n, err
	}
	if !f.Modified.IsZero() {
		os.Chtimes(target, f.Modified, f.Modified) // best effort: a wrong date harms nothing
	}
	return n, nil
}
