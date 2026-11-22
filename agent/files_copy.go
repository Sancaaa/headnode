package agent

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"headnode/protocol"
)

// This file holds the file manager operations that do more than one system call:
// copy, chmod and props. The small ones (list, mkdir, rename, delete) are in
// files.go.

// ------------------------------------------------------------------- copy

// copyPath copies a file, or a folder with everything in it, to a path that
// does not exist yet. Like rename it refuses to replace something: the browser
// picks a free name first. Symbolic links are copied as links, not followed, so
// a link inside a folder can never pull in a file from somewhere else.
func copyPath(from, to string) error {
	src, err := absPath(from)
	if err != nil {
		return err
	}
	dst, err := absPath(to)
	if err != nil {
		return err
	}
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dst); err == nil {
		return errExists
	}

	if !info.IsDir() {
		return copyOne(src, dst, info)
	}

	// Copying a folder into itself would never end: the copy keeps finding
	// itself as one of the things to copy.
	if rel, err := filepath.Rel(src, dst); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errIntoItself
	}
	// Count before starting, so a mistake (copying "/") is refused with nothing
	// done, not after half the disk has been written.
	if m := measure(src); m.truncated {
		return fmt.Errorf("too many files to copy at once (more than %d)", walkLimit)
	}

	// The folder itself is created here, and only here: if that fails, nothing of
	// ours exists yet, so nothing must be cleaned up. Everything after it is ours.
	if err := os.Mkdir(dst, 0o700); err != nil {
		return err
	}
	if err := copyTree(src, dst); err != nil {
		os.RemoveAll(dst)
		return err
	}
	return nil
}

// copyTree copies what is inside src into the existing folder dst.
func copyTree(src, dst string) error {
	type folder struct {
		path string
		perm fs.FileMode
	}
	var folders []folder

	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil || rel == "." {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			// Created writable for us, and given its real mode at the end: a
			// folder copied with mode 0555 could not be filled otherwise.
			folders = append(folders, folder{target, info.Mode().Perm()})
			return os.Mkdir(target, 0o700)
		case info.Mode()&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		default:
			return copyOne(path, target, info)
		}
	})
	if err != nil {
		return err
	}
	for i := len(folders) - 1; i >= 0; i-- { // deepest first
		if err := os.Chmod(folders[i].path, folders[i].perm); err != nil {
			return err
		}
	}
	return nil
}

// copyOne copies one regular file (or link) to a new path, keeping its mode.
// O_EXCL makes the create fail if anything appeared at dst in the meantime, so a
// copy can never overwrite.
func copyOne(src, dst string, info fs.FileInfo) error {
	if info.Mode()&fs.ModeSymlink != 0 {
		link, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(link, dst)
	}
	if !info.Mode().IsRegular() {
		return errSpecial
	}
	if isSecretFile(info) {
		return errSecretFile
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	// Check the opened file descriptor too so a swap after Lstat cannot slip through.
	if openedInfo, err := in.Stat(); err != nil {
		return err
	} else if isSecretFile(openedInfo) {
		return errSecretFile
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	// Chmod, not the mode given to OpenFile: the process umask would have
	// removed some bits from that.
	if err := os.Chmod(dst, info.Mode().Perm()); err != nil {
		os.Remove(dst)
		return err
	}
	return nil
}

// ------------------------------------------------------------------ chmod

// chmodPath sets the permission bits of one file or folder (not what is inside
// it). Only the nine rwx bits: the same rule as a sent file (D-33), so this
// cannot be used to make a program run as someone else.
func chmodPath(p, mode string) error {
	abs, err := absPath(p)
	if err != nil {
		return err
	}
	if mode == "" {
		return errBadMode // parseMode would read "" as "the default", which is wrong here
	}
	perm, err := parseMode(mode)
	if err != nil {
		return err
	}
	// Stat, not Lstat: os.Chmod follows symlinks, so a link to agent.json must
	// be refused just like agent.json itself.
	info, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if isSecretFile(info) {
		return errSecretFile
	}
	return os.Chmod(abs, perm)
}

// ------------------------------------------------------------------ props

// folderSize is what measure found in a folder.
type folderSize struct {
	bytes     int64
	files     int
	dirs      int
	truncated bool // there were more than walkLimit entries, so the rest was not counted
}

// measure adds up a folder: its files' bytes, and how many files and folders
// are inside at any depth. It does not follow links, and it skips what it
// cannot read (a folder without permission) instead of failing: a size that
// leaves out one locked folder is more useful than no answer.
func measure(root string) folderSize {
	var m folderSize
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable: skip it (for a folder, WalkDir then does not enter it)
		}
		if path == root {
			return nil
		}
		if m.files+m.dirs >= walkLimit {
			m.truncated = true
			return fs.SkipAll
		}
		if d.IsDir() {
			m.dirs++
			return nil
		}
		m.files++
		if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
			m.bytes += info.Size()
		}
		return nil
	})
	return m
}

// propsOf describes one file or folder for the Properties window. A folder's
// size is added up with measure, so it is the one read that can take a while.
func propsOf(p string) (protocol.FileResponse, error) {
	abs, err := absPath(p)
	if err != nil {
		return protocol.FileResponse{}, err
	}
	info, err := os.Lstat(abs) // a link is described itself, like stat does
	if err != nil {
		return protocol.FileResponse{}, err
	}

	props := protocol.FileProps{
		Name:    info.Name(),
		IsDir:   info.IsDir(),
		IsLink:  info.Mode()&fs.ModeSymlink != 0,
		Mode:    info.Mode().String(),
		Perm:    fmt.Sprintf("%04o", info.Mode().Perm()),
		ModTime: info.ModTime().UTC(),
		Size:    info.Size(),
	}
	props.Owner, props.Group = ownerOf(info)
	if props.IsLink {
		if target, err := os.Readlink(abs); err == nil {
			props.LinkTarget = target
		}
	}
	if props.IsDir {
		m := measure(abs)
		props.Size, props.Files, props.Dirs, props.Truncated = m.bytes, m.files, m.dirs, m.truncated
	}
	return protocol.FileResponse{Props: &props}, nil
}
