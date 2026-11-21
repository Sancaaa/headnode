package agent

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"unicode/utf8"

	"headnode/protocol"
)

// This file is the editor's two operations: read the text of a small file, and
// write it back.

var (
	errNotText    = errors.New("this is not a text file (it has binary data in it)")
	errTooBigEdit = fmt.Errorf("the file is too big to edit here (the limit is %d KB): download it instead", protocol.MaxEditBytes/1024)
	errChanged    = errors.New("the file changed on the host since it was opened: close this window and open it again")
	errNoVersion  = errors.New("the file's version is missing: open it again")
)

// versionOf says whether a file is still the one that was read. It is the
// modification time and the size, which change whenever the content is written.
// It is not a hash: reading a file again just to compare would make every save
// slower, and a change that keeps both the size and the time to the nanosecond
// is not something a person editing a file by hand will meet.
func versionOf(info fs.FileInfo) string {
	return fmt.Sprintf("%d-%d", info.ModTime().UnixNano(), info.Size())
}

// readText returns a small file's text and its version. Anything that is not
// plain text is refused, because the browser's text box would damage it when it
// was saved.
func readText(p string) (protocol.FileResponse, error) {
	abs, err := absPath(p)
	if err != nil {
		return protocol.FileResponse{}, err
	}
	file, err := os.Open(abs)
	if err != nil {
		return protocol.FileResponse{}, err
	}
	defer file.Close()
	info, err := file.Stat() // of what was opened: a link has been followed
	if err != nil {
		return protocol.FileResponse{}, err
	}
	if info.IsDir() {
		return protocol.FileResponse{}, errIsDir
	}
	if !info.Mode().IsRegular() {
		return protocol.FileResponse{}, errNotRegular // reading /dev/zero would never end
	}
	if isSecretFile(info) {
		return protocol.FileResponse{}, errSecretFile
	}
	if info.Size() > protocol.MaxEditBytes {
		return protocol.FileResponse{}, errTooBigEdit
	}

	// One byte more than the limit: a file that grew since the Stat is caught too.
	data, err := io.ReadAll(io.LimitReader(file, protocol.MaxEditBytes+1))
	if err != nil {
		return protocol.FileResponse{}, err
	}
	if len(data) > protocol.MaxEditBytes {
		return protocol.FileResponse{}, errTooBigEdit
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return protocol.FileResponse{}, errNotText
	}
	return protocol.FileResponse{Text: string(data), Version: versionOf(info)}, nil
}

// writeText replaces the text of an existing file, but only if it is still the
// version that was read: if someone changed it on the host in the meantime, the
// save is refused instead of silently throwing their change away.
//
// It writes into the file itself (not a new file renamed over it), so the
// owner, the mode, links and hard links all stay as they are. The price is that
// a crash in the middle of the write leaves a half-written file. The files this
// is for are small, and an editor on the host does the same.
func writeText(p, content, version string) (protocol.FileResponse, error) {
	abs, err := absPath(p)
	if err != nil {
		return protocol.FileResponse{}, err
	}
	if version == "" {
		return protocol.FileResponse{}, errNoVersion // a blind overwrite is never what the editor means
	}
	if len(content) > protocol.MaxEditBytes {
		return protocol.FileResponse{}, errTooBigEdit
	}
	info, err := os.Stat(abs) // through a link, like the file it points at
	if err != nil {
		return protocol.FileResponse{}, err
	}
	if !info.Mode().IsRegular() {
		return protocol.FileResponse{}, errNotRegular
	}
	if versionOf(info) != version {
		return protocol.FileResponse{}, errChanged
	}

	// No O_CREATE: the file exists, and a path that vanished is an error.
	file, err := os.OpenFile(abs, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return protocol.FileResponse{}, err
	}
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		return protocol.FileResponse{}, err
	}
	if err := file.Close(); err != nil {
		return protocol.FileResponse{}, err
	}
	after, err := os.Stat(abs)
	if err != nil {
		return protocol.FileResponse{}, err
	}
	return protocol.FileResponse{Version: versionOf(after)}, nil
}
