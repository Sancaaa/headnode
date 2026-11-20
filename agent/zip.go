package agent

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/coder/websocket"

	"headnode/protocol"
)

var errTooManyEntries = fmt.Errorf("the folder has more than %d entries, which is too many to zip", walkLimit)

// maxSkippedListed is how many skipped paths the archive's note names. A folder
// with a million unreadable files must not produce a note of its own that is
// bigger than the problem.
const maxSkippedListed = 200

// runZipDownload sends a folder as a zip archive, built while it is sent: the
// archive is never on the host's disk, and the agent holds one chunk of it at a
// time, so zipping a large folder does not need space or memory. It is the same
// `download` session as a single file (protocol.md §4), with an unknown size
// (-1) in the header, because the size of an archive is not known until it ends.
func runZipDownload(ctx context.Context, conn *websocket.Conn, root string) error {
	// We only write on this connection. CloseRead cancels ctx when it ends, so a
	// browser that cancelled the download stops the zipping at once.
	ctx = conn.CloseRead(ctx)

	header, err := json.Marshal(protocol.DownloadHeader{Name: zipName(root), Size: -1})
	if err != nil {
		return err
	}
	if err := conn.Write(ctx, websocket.MessageText, header); err != nil {
		return nil
	}

	out := &frameWriter{ctx: ctx, conn: conn}
	err = writeZip(ctx, out, root)
	switch {
	case err == nil:
		conn.Close(websocket.StatusNormalClosure, "")
		return nil
	case out.failed || ctx.Err() != nil:
		return nil // the other side is gone: nothing more to do
	default:
		// Say it plainly: a normal close would make a cut-off archive look complete.
		conn.Close(websocket.StatusInternalError, closeReason("zip error: "+describe(err)))
		return err
	}
}

// frameWriter turns what the zip writer writes into binary frames. failed says
// that a frame could not be sent, which means the browser is gone (not a problem
// with the folder).
type frameWriter struct {
	ctx    context.Context
	conn   *websocket.Conn
	failed bool
}

func (w *frameWriter) Write(p []byte) (int, error) {
	if err := w.conn.Write(w.ctx, websocket.MessageBinary, p); err != nil {
		w.failed = true
		return 0, err
	}
	return len(p), nil
}

// zipName is the archive's file name: the folder's name, plus ".zip".
func zipName(root string) string {
	return folderName(root) + ".zip"
}

// folderName is the last part of a path, or "root" for "/".
func folderName(root string) string {
	name := filepath.Base(root)
	if name == "/" || name == "." || name == string(filepath.Separator) {
		return "root"
	}
	return name
}

// writeZip writes the folder as a zip archive to w. Everything is inside one
// folder named like the original, so unpacking it never spills files into the
// folder the person happens to be in.
//
// Links, devices and other special files are not followed or stored. A file or
// folder that cannot be read (no permission) is left out, and the archive says
// so in a SKIPPED.txt at its top: an archive that quietly lacks files would be
// worse than no archive.
func writeZip(ctx context.Context, w io.Writer, root string) error {
	// A 64 KB buffer, so the archive goes out in chunks of that size and not in
	// the many tiny writes the zip format makes.
	buffered := bufio.NewWriterSize(w, downloadChunk)
	zw := zip.NewWriter(buffered)
	top := folderName(root)

	var skipped []string
	skip := func(rel, why string) { skipped = append(skipped, rel+" ("+why+")") }
	entries := 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if walkErr != nil {
			// Left out, and the archive goes on. (rel is "." when it is the folder
			// being zipped that cannot be read: then the archive is only the note.)
			skip(filepath.ToSlash(rel), describe(walkErr))
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		name := top
		if rel != "." {
			name = top + "/" + filepath.ToSlash(rel)
		}
		entries++
		if entries > walkLimit {
			return errTooManyEntries
		}

		info, err := d.Info()
		if err != nil {
			skip(filepath.ToSlash(rel), describe(err))
			return nil
		}
		switch {
		case info.IsDir():
			header, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			header.Name = name + "/"
			header.Method = zip.Store
			_, err = zw.CreateHeader(header)
			return err
		case info.Mode().IsRegular():
			return addFile(zw, path, name, info, func(why error) { skip(filepath.ToSlash(rel), describe(why)) })
		default:
			skip(filepath.ToSlash(rel), "not a regular file")
			return nil
		}
	})
	if err != nil {
		return err
	}

	if len(skipped) > 0 {
		note, err := zw.Create(top + "/SKIPPED.txt")
		if err != nil {
			return err
		}
		listed := skipped
		if len(listed) > maxSkippedListed {
			listed = listed[:maxSkippedListed]
		}
		text := "These were left out of the archive:\n" + strings.Join(listed, "\n") + "\n"
		if len(skipped) > len(listed) {
			text += fmt.Sprintf("...and %d more.\n", len(skipped)-len(listed))
		}
		if _, err := io.WriteString(note, text); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return buffered.Flush()
}

// addFile stores one regular file. A file that cannot be opened is skipped (and
// reported through skip). One that fails while it is being read is an error: the
// entry is already half written, so the archive cannot go on.
func addFile(zw *zip.Writer, path, name string, info fs.FileInfo, skip func(error)) error {
	file, err := os.Open(path)
	if err != nil {
		skip(err)
		return nil
	}
	defer file.Close()

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = name
	header.Method = zip.Deflate
	out, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, file); err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		return fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	return nil
}
