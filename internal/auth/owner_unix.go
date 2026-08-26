//go:build !windows

package auth

import (
	"io/fs"
	"os"
	"syscall"

	"github.com/stufently/zabbix-ai-cli-mcp/internal/errs"
)

func checkOwner(fi fs.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if uid := os.Getuid(); int(st.Uid) != uid {
		return errs.Usage("credential file is owned by uid %d, not by uid %d", st.Uid, uid)
	}
	return nil
}
