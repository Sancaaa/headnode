package agent

import (
	"io/fs"
	"os/user"
	"strconv"
	"syscall"
)

// ownerOf returns the owner and group names of a file. When a name cannot be
// looked up (a user that was deleted, a container without /etc/passwd entries)
// the number is shown instead, which is what `ls -l` does too.
func ownerOf(info fs.FileInfo) (owner, group string) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", ""
	}
	uid, gid := strconv.FormatUint(uint64(stat.Uid), 10), strconv.FormatUint(uint64(stat.Gid), 10)
	owner, group = uid, gid
	if u, err := user.LookupId(uid); err == nil {
		owner = u.Username
	}
	if g, err := user.LookupGroupId(gid); err == nil {
		group = g.Name
	}
	return owner, group
}
