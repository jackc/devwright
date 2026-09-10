package launch

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectorySnapshotWithoutGit(t *testing.T) {
	source := t.TempDir()
	writeTest(t, filepath.Join(source, "untracked"), "local changes")
	writeTest(t, filepath.Join(source, ".hidden"), "hidden")
	writeTest(t, filepath.Join(source, ".git/config"), "host metadata")
	if e := os.Symlink("untracked", filepath.Join(source, "link")); e != nil {
		t.Fatal(e)
	}
	// Any accidental Git command fails even on a machine with Git installed.
	bin := t.TempDir()
	writeTest(t, filepath.Join(bin, "git"), "#!/bin/sh\nexit 99\n")
	t.Setenv("PATH", bin)
	archive := filepath.Join(t.TempDir(), "source.tar.gz")
	if _, e := snapshotDirectory(source, archive); e != nil {
		t.Fatal(e)
	}
	f, e := os.Open(archive)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	gz, e := gzip.NewReader(f)
	if e != nil {
		t.Fatal(e)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	files := map[string]string{}
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if strings.HasPrefix(h.Name, ".git") {
			t.Fatal("copied Git metadata")
		}
		b, e := io.ReadAll(tr)
		if e != nil {
			t.Fatal(e)
		}
		files[h.Name] = string(b)
		if h.Name == "link" && (h.Typeflag != tar.TypeSymlink || h.Linkname != "untracked") {
			t.Fatal("symlink not preserved")
		}
		if h.Name == "untracked" && h.Mode&0100 == 0 {
			t.Fatal("lost executable mode")
		}
	}
	if files["untracked"] != "local changes" || files[".hidden"] != "hidden" {
		t.Fatalf("missing files: %v", files)
	}
}
func TestDirectoryAcceptance(t *testing.T) {
	name := os.Getenv("DEVWRIGHT_DIRECTORY_ACCEPTANCE")
	if name == "" {
		t.Skip("set DEVWRIGHT_DIRECTORY_ACCEPTANCE to a fresh disposable VM name")
	}
	a := testApp(t)
	a.out = os.Stdout
	a.err = os.Stderr
	project := filepath.Join(t.TempDir(), "full_stack_payments")
	writeTest(t, filepath.Join(project, ".devwright/lima.yaml"), `minimumLimaVersion: "2.2.0"
base:
  - template:_images/ubuntu-26.04
plain: true
mounts: []
cpus: 2
memory: 2GiB
user:
  name: dev
  home: /home/dev
  uid: 1000
  shell: /bin/bash
`)
	writeTest(t, filepath.Join(project, "local-script"), "#!/bin/bash\nprintf local-files\n")
	writeTest(t, filepath.Join(project, ".hidden"), "included")
	if e := os.Symlink("local-script", filepath.Join(project, "link")); e != nil {
		t.Fatal(e)
	}
	writeTest(t, filepath.Join(project, ".devwright/setup-project.sh"), "#!/bin/bash\nset -eu\ntest \"$(./link)\" = local-files\ntest \"$(cat .hidden)\" = included\ntest ! -e .git\nprintf hook >> hook-count\n")
	bin := t.TempDir()
	writeTest(t, filepath.Join(bin, "git"), "#!/bin/sh\necho 'Git must not be called for a local directory' >&2\nexit 99\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	defer a.run("limactl", "stop", name)
	if e := a.create(name, options{from: project, recipe: ".devwright/lima.yaml", installer: "install"}); e != nil {
		t.Fatal(e)
	}
	s, m, e := a.saved(name)
	if e != nil {
		t.Fatal(e)
	}
	if m.Project != nil || m.LocalProject == "" {
		t.Fatal("local directory recorded as Git")
	}
	if m.ProjectName != "full_stack_payments" {
		t.Fatalf("wrong saved project name: %q", m.ProjectName)
	}
	if e = a.ssh(s, "dev", "printf edited > \"$HOME/projects/"+m.ProjectName+"/local-script\"", nil, a.out); e != nil {
		t.Fatal(e)
	}
	// The source directory may disappear after create; finish must use saved state.
	if e = os.RemoveAll(project); e != nil {
		t.Fatal(e)
	}
	if e = a.finish(name); e != nil {
		t.Fatal(e)
	}
	if e = a.ssh(s, "dev", "test \"$(cat \"$HOME/projects/"+m.ProjectName+"/local-script\")\" = edited; test \"$(cat \"$HOME/projects/"+m.ProjectName+"/hook-count\")\" = hook", nil, a.out); e != nil {
		t.Fatal(e)
	}
	t.Log("PASS ordinary directory without Git, local/hidden/executable files, symlink, project hook, saved snapshot and preservation of guest edits")
}
