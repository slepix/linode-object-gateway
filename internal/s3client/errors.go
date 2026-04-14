package s3client

import (
	"errors"
	"strings"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TranslateError(err error) syscall.Errno {
	if err == nil {
		return 0
	}

	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return syscall.ENOENT
	}

	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return syscall.ENOENT
	}

	errMsg := err.Error()

	if strings.Contains(errMsg, "StatusCode: 404") || strings.Contains(errMsg, "NoSuchKey") {
		return syscall.ENOENT
	}
	if strings.Contains(errMsg, "StatusCode: 403") || strings.Contains(errMsg, "AccessDenied") {
		return syscall.EACCES
	}
	if strings.Contains(errMsg, "StatusCode: 409") {
		return syscall.EEXIST
	}
	if strings.Contains(errMsg, "SlowDown") || strings.Contains(errMsg, "StatusCode: 503") {
		return syscall.EAGAIN
	}

	return syscall.EIO
}
