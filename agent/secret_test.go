package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// D-62: the agent's credentials file is never sent, by any of the reads a viewer
// may use: the editor, a download, or a zipped folder.
func TestSecretFileIsNeverHandedOut(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "agent.json")
	if err := os.WriteFile(secret, []byte(`{"agent_secret":"s3cret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("fine to read"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := secretFile
	secretFile = secret
	t.Cleanup(func() { secretFile = old })

	if _, err := readText(secret); !errors.Is(err, errSecretFile) {
		t.Errorf("readText(agent.json): err = %v, want errSecretFile", err)
	}
	if file, _, _, err := openForDownload(secret); !errors.Is(err, errSecretFile) {
		if file != nil {
			file.Close()
		}
		t.Errorf("openForDownload(agent.json): err = %v, want errSecretFile", err)
	}
	// The file itself is compared, not its name: a link to it is refused too.
	link := filepath.Join(dir, "innocent.txt")
	if err := os.Symlink(secret, link); err == nil {
		if _, err := readText(link); !errors.Is(err, errSecretFile) {
			t.Errorf("readText(a link to agent.json): err = %v, want errSecretFile", err)
		}
	}
	// Other files are not affected.
	if resp, err := readText(filepath.Join(dir, "notes.txt")); err != nil || resp.Text != "fine to read" {
		t.Errorf("readText(notes.txt) = %q, %v", resp.Text, err)
	}

	var archive bytes.Buffer
	if err := writeZip(context.Background(), &archive, dir); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal(err)
	}
	sawNotes := false
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/agent.json") {
			t.Error("the zipped folder holds agent.json")
		}
		sawNotes = sawNotes || strings.HasSuffix(f.Name, "/notes.txt")
	}
	if !sawNotes {
		t.Error("the zipped folder lost notes.txt")
	}
}

func TestNoSecretFileProtectsNothing(t *testing.T) {
	old := secretFile
	secretFile = ""
	t.Cleanup(func() { secretFile = old })
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if isSecretFile(info) {
		t.Error("with no credentials file known, nothing should count as it")
	}
}
