package fs

import (
	"io/fs"
	"os"
	"path"
)

// NewDiskFS creates a new FileSystem rooted at the specified directory
func NewDiskFS(dir string) FileSystem {
	return dirFS(dir)
}

// dirFS implements FileSystem for a specific directory
type dirFS string

// OpenFile opens a file with the specified flags and permissions
func (dir dirFS) OpenFile(name string, flag int, perm fs.FileMode) (FileWriter, error) {
	return os.OpenFile(dir.join(name), flag, perm)
}

func (dir dirFS) Mkdir(name string, perm fs.FileMode) error {
	return os.Mkdir(dir.join(name), perm)
}

func (dir dirFS) MkdirAll(name string, perm fs.FileMode) error {
	return os.MkdirAll(dir.join(name), perm)
}

func (dir dirFS) Remove(name string) error {
	return os.Remove(dir.join(name))
}

func (dir dirFS) RemoveAll(name string) error {
	return os.RemoveAll(dir.join(name))
}

// Open opens a file for reading
func (dir dirFS) Open(name string) (fs.File, error) {
	return os.Open(dir.join(name))
}

// ReadFile reads the entire contents of a file
func (dir dirFS) ReadFile(name string) ([]byte, error) {
	return os.ReadFile(dir.join(name))
}

// ReadDir reads the contents of a directory
func (dir dirFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return os.ReadDir(dir.join(name))
}

// Stat returns file information
func (dir dirFS) Stat(name string) (fs.FileInfo, error) {
	return os.Stat(dir.join(name))
}

// Lstat returns file information without following symbolic links
func (dir dirFS) Lstat(name string) (fs.FileInfo, error) {
	return os.Lstat(dir.join(name))
}

// join constructs a full path by joining the directory and name
func (dir dirFS) join(name string) string {
	return path.Join(".", string(dir), name)
}
