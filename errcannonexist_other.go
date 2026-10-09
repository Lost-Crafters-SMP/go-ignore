// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package ignore

import (
	"errors"
	"syscall"
)

// pathCannotExist reports whether err indicates that the path's syntax can
// never name a file on this platform (a non-directory inside the path), as
// opposed to a file that simply is not present.
func pathCannotExist(err error) bool {
	return errors.Is(err, syscall.ENOTDIR)
}
