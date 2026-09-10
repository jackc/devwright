package launch

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Project names are directory components, independent of Lima's VM names.
// Keep shell metacharacters and path separators out of saved guest paths.
var validProjectName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$`)

func validateProjectName(name string) error {
	if !validProjectName.MatchString(name) {
		return errors.New("project name must be 1–255 letters, digits, dots, underscores, or hyphens, starting with a letter or digit")
	}
	return nil
}

func deriveProjectName(source string, local bool) (string, error) {
	var name string
	if local {
		absolute, err := filepath.Abs(source)
		if err != nil {
			return "", err
		}
		name = filepath.Base(absolute)
	} else {
		location := source
		if strings.Contains(source, "://") {
			u, err := url.Parse(source)
			if err != nil {
				// Do not include the URL: it may contain authentication details.
				return "", errors.New("cannot derive project name from repository URL; use --project-name")
			}
			location = u.Path
		} else if i := strings.Index(source, "]:"); i >= 0 {
			// SCP-style SSH with an IPv6 host: git@[::1]:team/project.git.
			location = source[i+2:]
		} else if _, remotePath, ok := strings.Cut(source, ":"); ok {
			// SCP-style SSH: git@github.com:team/project.git.
			location = remotePath
		}
		name = strings.TrimSuffix(path.Base(strings.TrimRight(location, "/")), ".git")
	}
	if err := validateProjectName(name); err != nil {
		return "", fmt.Errorf("cannot derive project name from --from; use --project-name: %w", err)
	}
	return name, nil
}
