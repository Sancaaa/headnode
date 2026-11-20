//go:build !linux

package agent

import "io/fs"

// Only Linux is implemented (D-12): this stub keeps the package compiling on the
// Windows laptop, where there is no owner to show.
func ownerOf(info fs.FileInfo) (owner, group string) {
	return "", ""
}
