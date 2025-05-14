package fs

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// Copied from https://github.com/liamg/memoryfs and modified

// memFS is an in-memory filesystem
type memFS struct {
	dir *dir
}

// NewMemFS creates a new filesystem
func NewMemFS() FileSystem {
	return newMemFS()
}

func newMemFS() *memFS {
	return &memFS{
		dir: &dir{
			info: fileinfo{
				name:     ".",
				size:     0x100,
				modified: time.Now(),
				mode:     0777 | fs.ModeDir,
			},
			dirs:  map[string]*dir{},
			files: map[string]*file{},
		},
	}
}

// Stat returns a FileInfo describing the file.
func (m *memFS) Stat(name string) (fs.FileInfo, error) {
	name = cleanse(name)
	if f, err := m.dir.getFile(name); err == nil {
		return f.stat(), nil
	}
	if f, err := m.dir.getDir(name); err == nil {
		return f.Stat()
	}
	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}

// ReadDir reads the named directory
// and returns a list of directory entries sorted by filename.
func (m *memFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return m.dir.ReadDir(cleanse(name))
}

// Open opens the named file for reading.
func (m *memFS) Open(name string) (fs.File, error) {
	return m.dir.Open(cleanse(name))
}

// WriteFile writes the specified bytes to the named file. If the file exists, it will be overwritten.
func (m *memFS) WriteFile(path string, data []byte, perm fs.FileMode) error {
	return m.dir.WriteFile(cleanse(path), data, perm)
}

func (m *memFS) Lstat(name string) (fs.FileInfo, error) {
	return nil, fs.ErrInvalid
}

func (m *memFS) OpenFile(name string, flag int, perm fs.FileMode) (FileWriter, error) {
	name = cleanse(name)

	// Check if file exists
	if f, err := m.dir.getFile(name); err == nil {
		// If O_TRUNC is set, truncate the file
		if flag&os.O_TRUNC != 0 {
			if err := m.dir.WriteFile(name, []byte{}, perm); err != nil {
				return nil, err
			}
		}
		return f.open()
	}

	// If O_CREATE is set, create new file
	if flag&os.O_CREATE != 0 {
		if err := m.dir.WriteFile(name, []byte{}, perm); err != nil {
			return nil, err
		}
		if f, err := m.dir.getFile(name); err == nil {
			return f.open()
		}
	}

	return nil, &fs.PathError{Op: "openfile", Path: name, Err: fs.ErrNotExist}
}

// MkdirAll creates a directory named path,
// along with any necessary parents, and returns nil,
// or else returns an error.
// The permission bits perm (before umask) are used for all
// directories that MkdirAll creates.
// If path is already a directory, MkdirAll does nothing
// and returns nil.
func (m *memFS) MkdirAll(path string, perm fs.FileMode) error {
	return m.dir.MkdirAll(cleanse(path), perm)
}

// ReadFile reads the named file and returns its contents.
// A successful call returns a nil error, not io.EOF.
// (Because ReadFile reads the whole file, the expected EOF
// from the final Read is not treated as an error to be reported.)
//
// The caller is permitted to modify the returned byte slice.
// This method should return a copy of the underlying data.
func (m *memFS) ReadFile(name string) ([]byte, error) {
	f, err := m.dir.Open(cleanse(name))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// writeLazyFile creates (or overwrites) the named file.
// The contents of the file are not set at this time, but are read on-demand later using the provided LazyOpener.
func (m *memFS) writeLazyFile(path string, opener lazyOpener, perm fs.FileMode) error {
	return m.dir.writeLazyFile(cleanse(path), opener, perm)
}

// Remove deletes a file or directory from the filesystem
func (m *memFS) Remove(path string) error {
	return m.dir.Remove(cleanse(path))
}

// RemoveAll deletes a file or directory and any children if present from the filesystem
func (m *memFS) RemoveAll(path string) error {
	return m.dir.RemoveAll(cleanse(path))
}

type fileinfo struct {
	name     string
	size     int64
	modified time.Time
	mode     fs.FileMode
	sys      interface{}
}

// Name is the base name of the file (without directory)
func (f fileinfo) Name() string {
	return f.name
}

// Size is the size of the file in bytes (not reliable for lazy loaded files)
func (f fileinfo) Size() int64 {
	return f.size
}

// Mode is the fs.FileMode of the file
func (f fileinfo) Mode() fs.FileMode {
	return f.mode
}

// Info attempts to provide the fs.FileInfo for the file
func (f fileinfo) Info() (fs.FileInfo, error) {
	return f, nil
}

// Type returns the type bits for the entry.
// The type bits are a subset of the usual FileMode bits, those returned by the FileMode.Type method.
func (f fileinfo) Type() fs.FileMode {
	return f.Mode().Type()
}

// ModTime is the modification time of the file (not reliable for lazy loaded files)
func (f fileinfo) ModTime() time.Time {
	return f.modified
}

// IsDir reports whether the entry describes a directory.
func (f fileinfo) IsDir() bool {
	return f.Mode().IsDir()
}

// Sys is the underlying data source of the file (can return nil)
func (f fileinfo) Sys() interface{} {
	return f.sys
}

type file struct {
	sync.RWMutex
	info    fileinfo
	opener  lazyOpener
	content []byte
}

type fileAccess struct {
	file   *file
	reader io.Reader
}

// lazyOpener provides an io.Reader that can be used to access the content of a file, whatever the actual storage medium.
// If the lazyOpener returns an io.ReadCloser, it will be closed after each read.
type lazyOpener func() (io.Reader, error)

const bufferSize = 0x100

func (f *file) overwrite(data []byte, perm fs.FileMode) error {

	f.RLock()
	if f.opener == nil {
		f.RUnlock()
		return fmt.Errorf("missing opener")
	}
	f.RUnlock()

	rw, err := f.open()
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}

	f.Lock()
	f.info.size = int64(len(data))
	f.info.modified = time.Now()
	f.info.mode = perm
	f.Unlock()

	for len(data) > 0 {
		n, err := rw.Write(data)
		if err != nil {
			return err
		}
		data = data[n:]
	}

	return nil
}

func (f *file) stat() fs.FileInfo {
	f.RLock()
	defer f.RUnlock()
	return f.info
}

func (f *file) open() (*fileAccess, error) {
	f.RLock()
	defer f.RUnlock()
	if f.opener == nil {
		return nil, fmt.Errorf("missing opener")
	}
	return &fileAccess{
		file: f,
	}, nil
}

func (f *fileAccess) Stat() (fs.FileInfo, error) {
	f.file.RLock()
	defer f.file.RUnlock()
	return f.file.info, nil
}

func (f *fileAccess) Read(data []byte) (int, error) {
	r, err := func() (io.Reader, error) {
		f.file.Lock()
		defer f.file.Unlock()
		if f.reader == nil {
			r, err := f.file.opener()
			if err != nil {
				return nil, fmt.Errorf("failed to read file: %w", err)
			}
			f.reader = r
		}
		return f.reader, nil
	}()
	if err != nil {
		return 0, err
	}
	return r.Read(data)
}

func (f *fileAccess) Close() error {
	f.file.Lock()
	defer f.file.Unlock()
	if f.reader == nil {
		return nil
	}
	if closer, ok := f.reader.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (f *fileAccess) Write(p []byte) (n int, err error) {
	w, err := func() (io.Writer, error) {
		f.file.Lock()
		defer f.file.Unlock()
		if f.reader == nil {
			r, err := f.file.opener()
			if err != nil {
				return nil, fmt.Errorf("failed to read file: %w", err)
			}
			f.reader = r
		}
		w, ok := f.reader.(io.Writer)
		if !ok {
			return nil, fmt.Errorf("cannot write - opener did not provide io.Writer")
		}
		return w, nil
	}()
	if err != nil {
		return 0, err
	}
	return w.Write(p)
}

type lazyAccess struct {
	file   *file
	reader io.Reader
	writer *bytes.Buffer
}

func (l *lazyAccess) Read(data []byte) (int, error) {
	l.file.RLock()
	defer l.file.RUnlock()
	if l.reader == nil {
		l.reader = bytes.NewReader(l.file.content)
	}
	return l.reader.Read(data)
}

func (l *lazyAccess) Write(data []byte) (int, error) {
	l.file.Lock()
	defer l.file.Unlock()
	if l.writer == nil {
		l.writer = bytes.NewBuffer(l.file.content)
		l.writer.Reset()
	}
	n, err := l.writer.Write(data)
	if err != nil {
		return 0, err
	}
	l.file.content = l.writer.Bytes()
	return n, nil
}

var separator = "/"

type dir struct {
	sync.RWMutex
	info  fileinfo
	dirs  map[string]*dir
	files map[string]*file
}

func (d *dir) Open(name string) (fs.File, error) {

	if name == "" || name == "." {
		return d, nil
	}

	if f, err := d.getFile(name); err == nil {
		return f.open()
	}

	if f, err := d.getDir(name); err == nil {
		return f, nil
	}

	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

func (d *dir) Remove(name string) error {
	if name == "" || name == "." {
		return nil
	}

	return d.removePath(name, false)
}

func (d *dir) RemoveAll(name string) error {
	if name == "" || name == "." {
		return nil
	}

	return d.removePath(name, true)
}

func (d *dir) Stat() (fs.FileInfo, error) {
	d.RLock()
	defer d.RUnlock()
	return d.info, nil
}

func (d *dir) removePath(name string, recursive bool) error {

	parts := strings.Split(name, separator)
	if len(parts) == 1 {
		d.RLock()
		_, ok := d.files[name]
		d.RUnlock()
		if ok {
			delete(d.files, name)
			return nil
		}

		if sub, err := d.getDir(parts[0]); err == nil {
			d.Lock()
			defer d.Unlock()
			if len(sub.dirs) == 0 && len(sub.files) == 0 {
				delete(d.dirs, parts[0])
				return nil
			} else if recursive {
				for _, s := range sub.dirs {
					sub.removePath(s.info.name, recursive)
				}
				for _, f := range sub.files {
					sub.removePath(f.info.name, recursive)
				}
				delete(d.dirs, parts[0])
				return nil
			}
			return fs.ErrInvalid
		}
		return fs.ErrNotExist
	}

	sub, err := d.getDir(parts[0])
	if err != nil {
		return err
	}

	return sub.removePath(strings.Join(parts[1:], separator), recursive)
}

func (d *dir) getFile(name string) (*file, error) {

	parts := strings.Split(name, separator)
	if len(parts) == 1 {
		d.RLock()
		f, ok := d.files[name]
		d.RUnlock()
		if ok {
			return f, nil
		}
		return nil, fs.ErrNotExist
	}

	sub, err := d.getDir(parts[0])
	if err != nil {
		return nil, err
	}

	return sub.getFile(strings.Join(parts[1:], separator))
}

func (d *dir) getDir(name string) (*dir, error) {

	if name == "" {
		return d, nil
	}

	parts := strings.Split(name, separator)

	d.RLock()
	f, ok := d.dirs[parts[0]]
	d.RUnlock()
	if ok {
		return f.getDir(strings.Join(parts[1:], separator))
	}

	return nil, fs.ErrNotExist
}

func (d *dir) ReadDir(name string) ([]fs.DirEntry, error) {
	if name == "" {
		var entries []fs.DirEntry
		d.RLock()
		for _, file := range d.files {
			stat := file.stat()
			entries = append(entries, stat.(fs.DirEntry))
		}
		for _, dir := range d.dirs {
			stat, _ := dir.Stat()
			entries = append(entries, stat.(fs.DirEntry))
		}
		d.RUnlock()
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		return entries, nil
	}

	parts := strings.Split(name, separator)

	d.RLock()
	dir, ok := d.dirs[parts[0]]
	d.RUnlock()
	if !ok {
		return nil, fs.ErrNotExist
	}
	return dir.ReadDir(strings.Join(parts[1:], separator))
}

func (d *dir) Read(_ []byte) (int, error) {
	return 0, fs.ErrInvalid
}

func (d *dir) Close() error {
	return nil
}

func (d *dir) MkdirAll(path string, perm fs.FileMode) error {
	parts := strings.Split(path, separator)

	if path == "" {
		return nil
	}

	d.RLock()
	_, ok := d.files[parts[0]]
	d.RUnlock()
	if ok {
		return fs.ErrExist
	}

	d.Lock()
	if perm&fs.ModeDir == 0 {
		perm |= fs.ModeDir
	}
	if _, ok := d.dirs[parts[0]]; !ok {
		d.dirs[parts[0]] = &dir{
			info: fileinfo{
				name:     parts[0],
				size:     0x100,
				modified: time.Now(),
				mode:     perm,
			},
			dirs:  map[string]*dir{},
			files: map[string]*file{},
		}
	}
	d.info.modified = time.Now()
	d.Unlock()

	if len(parts) == 1 {
		return nil
	}

	d.RLock()
	defer d.RUnlock()
	return d.dirs[parts[0]].MkdirAll(strings.Join(parts[1:], separator), perm)
}

func (d *dir) WriteFile(path string, data []byte, perm fs.FileMode) error {
	parts := strings.Split(path, separator)

	if perm&fs.ModeDir != 0 {
		return fmt.Errorf("invalid perm: %v", perm)
	}

	if len(parts) == 1 {
		max := bufferSize
		if len(data) > max {
			max = len(data)
		}
		buffer := make([]byte, len(data), max)
		copy(buffer, data)
		d.Lock()
		defer d.Unlock()
		if existing, ok := d.files[parts[0]]; ok {
			if err := existing.overwrite(buffer, perm); err != nil {
				return err
			}
		} else {
			newFile := &file{
				info: fileinfo{
					name:     parts[0],
					size:     int64(len(buffer)),
					modified: time.Now(),
					mode:     perm,
				},
				content: buffer,
			}
			newFile.opener = func() (io.Reader, error) {
				return &lazyAccess{
					file: newFile,
				}, nil
			}
			d.files[parts[0]] = newFile
		}
		return nil
	}

	d.RLock()
	_, ok := d.dirs[parts[0]]
	d.RUnlock()
	if !ok {
		return fs.ErrNotExist
	}

	d.RLock()
	defer d.RUnlock()
	return d.dirs[parts[0]].WriteFile(strings.Join(parts[1:], separator), data, perm)
}

func (d *dir) writeLazyFile(path string, opener lazyOpener, perm fs.FileMode) error {
	parts := strings.Split(path, separator)

	if perm&fs.ModeDir != 0 {
		return fmt.Errorf("invalid perm: %v", perm)
	}

	if len(parts) == 1 {
		d.Lock()
		defer d.Unlock()
		d.files[parts[0]] = &file{
			info: fileinfo{
				name:     parts[0],
				size:     0,
				modified: time.Now(),
				mode:     perm,
			},
			opener: opener,
		}
		return nil
	}

	d.RLock()
	_, ok := d.dirs[parts[0]]
	d.RUnlock()
	if !ok {
		return fs.ErrNotExist
	}

	d.RLock()
	defer d.RUnlock()
	return d.dirs[parts[0]].writeLazyFile(strings.Join(parts[1:], separator), opener, perm)
}

func cleanse(p string) string {
	p = path.Clean(p)
	p = strings.TrimPrefix(p, "."+separator)
	p = strings.TrimPrefix(p, separator)
	if p == "." {
		return ""
	}
	return p
}
