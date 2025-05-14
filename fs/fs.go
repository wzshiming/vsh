package fs

import (
	"io"
	"io/fs"
)

// FileWriter combines fs.File and io.Writer interfaces for writable files
type FileWriter interface {
	fs.File
	io.Writer
}

// FileSystem defines the interface for file system operations
type FileSystem interface {
	fs.ReadDirFS
	fs.ReadFileFS
	fs.StatFS

	OpenFile(name string, flag int, perm fs.FileMode) (FileWriter, error)
	Open(name string) (fs.File, error)

	Lstat(name string) (fs.FileInfo, error)

	MkdirAll(name string, perm fs.FileMode) error

	Remove(name string) error

	RemoveAll(name string) error
}

// SnapshotFS allows you to take on fs.FS and wrap it in an fs that is writable
func SnapshotFS(base fs.FS) FileSystem {
	newFS := newMemFS()
	fs.WalkDir(base, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			return newFS.MkdirAll(path, d.Type().Perm())
		}

		// Lazy write the files, holding onto the base FS to read the content on demand
		return newFS.writeLazyFile(path, func() (io.Reader, error) {
			return base.Open(path)
		}, d.Type().Perm())
	})

	return newFS
}
