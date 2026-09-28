//go:build !linux

package workshop

import (
	"errors"
	"os"
)

// Fail closed on platforms without the Linux descriptor-walk implementation.
func openArtifactDownload(workspace, path string) (*os.File, error) {
	return nil, errors.New("artifact download requires Linux")
}
