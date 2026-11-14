package resticprofile

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/1k-off/reksicherung/internal/reksicherung/configgen/input"
)

// repositoryURL maps shared storage settings to a restic S3 repository URL.
// Kept isolated so future storage backends can plug in without rewriting
// profile assembly.
func repositoryURL(storage input.Storage, pathSuffix string) (string, error) {
	switch strings.ToLower(storage.Type) {
	case "s3", "minio":
		return s3RepositoryURL(storage, pathSuffix)
	default:
		return "", fmt.Errorf("resticprofile: storage.type %q has no repository mapping", storage.Type)
	}
}

func s3RepositoryURL(storage input.Storage, pathSuffix string) (string, error) {
	objectPath := input.JoinStoragePath(storage.PathPrefix, pathSuffix)
	var hostPart string
	if storage.Endpoint != "" {
		u, err := url.Parse(storage.Endpoint)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return "", errors.New("resticprofile: storage.endpoint is invalid")
		}
		hostPart = strings.TrimRight(u.Scheme+"://"+u.Host+strings.TrimRight(u.Path, "/"), "/")
	} else {
		hostPart = "s3.amazonaws.com"
	}
	repo := "s3:" + hostPart + "/" + storage.Bucket
	if objectPath != "" {
		repo += "/" + objectPath
	}
	return repo, nil
}
