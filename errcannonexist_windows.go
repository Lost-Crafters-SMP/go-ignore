// SPDX-License-Identifier: Apache-2.0

//go:build windows

package ignore

import (
	"errors"
	"syscall"
)

// pathCannotExist reports whether err indicates that the path's syntax can
// never name a file on this platform (forbidden filename characters, or
// bytes unrepresentable in the UTF-16 filesystem namespace), as opposed to a
// file that simply is not present.
func pathCannotExist(err error) bool {
	return errors.Is(err, errInvalidName) || errors.Is(err, errNoUnicodeTranslation)
}

// errInvalidName is ERROR_INVALID_NAME: "The filename, directory name, or
// volume label syntax is incorrect." errNoUnicodeTranslation is
// ERROR_NO_UNICODE_TRANSLATION (1113): the path holds bytes with no mapping
// into the UTF-16 filesystem namespace. Stdlib syscall exports neither.
const (
	errInvalidName          = syscall.Errno(123)
	errNoUnicodeTranslation = syscall.Errno(1113)
)
