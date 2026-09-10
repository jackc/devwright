package launch

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// snapshotDirectory copies files as they are, without invoking Git or following
// symlinks. Git metadata is omitted, including worktree pointers into the host.
func snapshotDirectory(source, archive string) (string, error) {
	source, e := filepath.Abs(source)
	if e != nil {
		return "", e
	}
	root, e := os.OpenRoot(source)
	if e != nil {
		return "", e
	}
	defer root.Close()
	f, e := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return "", e
	}
	defer f.Close()
	sum := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(f, sum))
	tw := tar.NewWriter(gz)
	e = fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if path == "." {
			return nil
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		// The temporary snapshot may be inside a source such as /tmp; don't archive
		// our staging directory into itself.
		if filepath.Join(source, filepath.FromSlash(path)) == filepath.Dir(archive) {
			return fs.SkipDir
		}
		info, e := entry.Info()
		if e != nil {
			return e
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, e = root.Readlink(path)
			if e != nil {
				return e
			}
		}
		if !info.Mode().IsRegular() && !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("cannot copy special file %s", path)
		}
		h, e := tar.FileInfoHeader(info, link)
		if e != nil {
			return e
		}
		h.Name = path
		h.Uid = 0
		h.Gid = 0
		h.Uname = ""
		h.Gname = ""
		if e = tw.WriteHeader(h); e != nil {
			return e
		}
		if info.Mode().IsRegular() {
			in, e := root.Open(path)
			if e != nil {
				return e
			}
			_, e = io.Copy(tw, in)
			ce := in.Close()
			if e != nil {
				return e
			}
			return ce
		}
		return nil
	})
	te := tw.Close()
	ge := gz.Close()
	fe := f.Close()
	for _, err := range []error{e, te, ge, fe} {
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func (a *app) installDirectory(s instance, projectName, digest string) error {
	raw, e := hex.DecodeString(digest)
	if e != nil || len(raw) != sha256.Size {
		return fmt.Errorf("invalid saved directory snapshot")
	}
	f, e := os.Open(filepath.Join(s.Dir, "devwright/project.tar.gz"))
	if e != nil {
		return e
	}
	defer f.Close()
	return a.ssh(s, s.Config.User.Name, directoryScript(projectName, digest), f, a.out)
}
func directoryScript(name, digest string) string {
	return `set -euo pipefail
umask 077
target="$HOME/projects/` + name + `"
marker="$HOME/.local/state/devwright/directory-source"
if [ -f "$marker" ]; then
  test "$(cat "$marker")" = ` + quote(digest) + `
  cat >/dev/null
  exit 0
fi
mkdir -p "$HOME/projects" "$(dirname "$marker")"
stage=$(mktemp -d "$HOME/projects/.devwright-transfer.XXXXXXXX")
trap 'rm -rf -- "$stage"' EXIT
cat > "$stage/project.tar.gz"
test "$(sha256sum "$stage/project.tar.gz" | cut -d ' ' -f 1)" = ` + quote(digest) + `
# A stamp inside the staged directory closes the rename/marker recovery gap.
stamp=.devwright-directory-source
if [ ! -e "$target" ] && [ ! -L "$target" ]; then
  mkdir "$stage/project"
  tar --extract --gzip --no-same-owner --file "$stage/project.tar.gz" --directory "$stage/project"
  test ! -e "$stage/project/$stamp" && test ! -L "$stage/project/$stamp"
  printf '%s' ` + quote(digest) + ` > "$stage/project/$stamp"
  mv -T "$stage/project" "$target"
fi
test "$(cat "$target/$stamp")" = ` + quote(digest) + `
printf '%s' ` + quote(digest) + ` > "$marker"
rm -- "$target/$stamp"
`
}
