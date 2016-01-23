package mem

import (
	"bytes"
	"errors"
	"io"
	"sort"
	"sync"

	"github.com/restic/restic/backend"
	"github.com/restic/restic/debug"
)

type entry struct {
	Type backend.Type
	Name string
}

type memMap map[entry][]byte

// MemoryBackend is a mock backend that uses a map for storing all data in
// memory. This should only be used for tests.
type MemoryBackend struct {
	data memMap
	m    sync.Mutex

	backend.MockBackend
}

// New returns a new backend that saves all data in a map in memory.
func New() *MemoryBackend {
	be := &MemoryBackend{
		data: make(memMap),
	}

	be.MockBackend.TestFn = func(t backend.Type, name string) (bool, error) {
		return memTest(be, t, name)
	}

	be.MockBackend.CreateFn = func() (backend.Blob, error) {
		return memCreate(be)
	}

	be.MockBackend.GetReaderFn = func(t backend.Type, name string, offset, length uint) (io.ReadCloser, error) {
		return memGetReader(be, t, name, offset, length)
	}

	be.MockBackend.LoadFn = func(h backend.Handle, p []byte, off int64) (int, error) {
		return memLoad(be, h, p, off)
	}

	be.MockBackend.StatFn = func(h backend.Handle) (backend.BlobInfo, error) {
		return memStat(be, h)
	}

	be.MockBackend.RemoveFn = func(t backend.Type, name string) error {
		return memRemove(be, t, name)
	}

	be.MockBackend.ListFn = func(t backend.Type, done <-chan struct{}) <-chan string {
		return memList(be, t, done)
	}

	be.MockBackend.DeleteFn = func() error {
		be.m.Lock()
		defer be.m.Unlock()

		be.data = make(memMap)
		return nil
	}

	be.MockBackend.LocationFn = func() string {
		return "Memory Backend"
	}

	debug.Log("MemoryBackend.New", "created new memory backend")

	return be
}

func (be *MemoryBackend) insert(t backend.Type, name string, data []byte) error {
	be.m.Lock()
	defer be.m.Unlock()

	if _, ok := be.data[entry{t, name}]; ok {
		return errors.New("already present")
	}

	be.data[entry{t, name}] = data
	return nil
}

func memTest(be *MemoryBackend, t backend.Type, name string) (bool, error) {
	be.m.Lock()
	defer be.m.Unlock()

	debug.Log("MemoryBackend.Test", "test %v %v", t, name)

	if _, ok := be.data[entry{t, name}]; ok {
		return true, nil
	}

	return false, nil
}

// tempMemEntry temporarily holds data written to the memory backend before it
// is finalized.
type tempMemEntry struct {
	be   *MemoryBackend
	data bytes.Buffer
}

func (e *tempMemEntry) Write(p []byte) (int, error) {
	return e.data.Write(p)
}

func (e *tempMemEntry) Size() uint {
	return uint(len(e.data.Bytes()))
}

func (e *tempMemEntry) Finalize(t backend.Type, name string) error {
	if t == backend.Config {
		name = ""
	}

	debug.Log("MemoryBackend", "save blob %p (%d bytes) as %v %v", e, len(e.data.Bytes()), t, name)
	return e.be.insert(t, name, e.data.Bytes())
}

func memCreate(be *MemoryBackend) (backend.Blob, error) {
	blob := &tempMemEntry{be: be}
	debug.Log("MemoryBackend.Create", "create new blob %p", blob)
	return blob, nil
}

func memGetReader(be *MemoryBackend, t backend.Type, name string, offset, length uint) (io.ReadCloser, error) {
	be.m.Lock()
	defer be.m.Unlock()

	if t == backend.Config {
		name = ""
	}

	debug.Log("MemoryBackend.GetReader", "get %v %v offset %v len %v", t, name, offset, length)

	if _, ok := be.data[entry{t, name}]; !ok {
		return nil, errors.New("no such data")
	}

	buf := be.data[entry{t, name}]
	if offset > uint(len(buf)) {
		return nil, errors.New("offset beyond end of file")
	}

	buf = buf[offset:]

	if length > 0 {
		if length > uint(len(buf)) {
			length = uint(len(buf))
		}

		buf = buf[:length]
	}

	return backend.ReadCloser(bytes.NewReader(buf)), nil
}

func memLoad(be *MemoryBackend, h backend.Handle, p []byte, off int64) (int, error) {
	be.m.Lock()
	defer be.m.Unlock()

	if err := h.Valid(); err != nil {
		return 0, err
	}

	if h.Type == backend.Config {
		h.Name = ""
	}

	debug.Log("MemoryBackend.Load", "get %v offset %v len %v", h, off, len(p))

	if _, ok := be.data[entry{h.Type, h.Name}]; !ok {
		return 0, errors.New("no such data")
	}

	buf := be.data[entry{h.Type, h.Name}]
	if off > int64(len(buf)) {
		return 0, errors.New("offset beyond end of file")
	}

	buf = buf[off:]

	n := copy(p, buf)

	if len(p) > len(buf) {
		return n, io.ErrUnexpectedEOF
	}

	return n, nil
}

func memStat(be *MemoryBackend, h backend.Handle) (backend.BlobInfo, error) {
	be.m.Lock()
	defer be.m.Unlock()

	if err := h.Valid(); err != nil {
		return backend.BlobInfo{}, err
	}

	if h.Type == backend.Config {
		h.Name = ""
	}

	debug.Log("MemoryBackend.Stat", "stat %v", h)

	e, ok := be.data[entry{h.Type, h.Name}]
	if !ok {
		return backend.BlobInfo{}, errors.New("no such data")
	}

	return backend.BlobInfo{Size: int64(len(e))}, nil
}

func memRemove(be *MemoryBackend, t backend.Type, name string) error {
	be.m.Lock()
	defer be.m.Unlock()

	debug.Log("MemoryBackend.Remove", "get %v %v", t, name)

	if _, ok := be.data[entry{t, name}]; !ok {
		return errors.New("no such data")
	}

	delete(be.data, entry{t, name})

	return nil
}

func memList(be *MemoryBackend, t backend.Type, done <-chan struct{}) <-chan string {
	be.m.Lock()
	defer be.m.Unlock()

	ch := make(chan string)

	var ids []string
	for entry := range be.data {
		if entry.Type != t {
			continue
		}
		ids = append(ids, entry.Name)
	}

	sort.Strings(ids)

	debug.Log("MemoryBackend.List", "list %v: %v", t, ids)

	go func() {
		defer close(ch)
		for _, id := range ids {
			select {
			case ch <- id:
			case <-done:
				return
			}
		}
	}()

	return ch
}
