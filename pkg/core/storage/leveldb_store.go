package storage

import (
	"sync"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/filter"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// LevelDBOptions configuration for LevelDB.
type LevelDBOptions struct {
	DataDirectoryPath string `yaml:"DataDirectoryPath"`
}

// LevelDBStore is the official storage implementation for storing and retrieving
// blockchain data.
type LevelDBStore struct {
	db      *leveldb.DB
	mtx     sync.Mutex
	batches []*leveldb.Batch
	path    string
}

// NewLevelDBStore returns a new LevelDBStore object that will
// initialize the database found at the given path.
func NewLevelDBStore(cfg LevelDBOptions) (*LevelDBStore, error) {
	var opts = new(opt.Options) // should be exposed via LevelDBOptions if anything needed

	opts.Filter = filter.NewBloomFilter(10)
	opts.DisableLargeBatchTransaction = true
	opts.DisableSeeksCompaction = true
	db, err := leveldb.OpenFile(cfg.DataDirectoryPath, opts)
	if err != nil {
		return nil, err
	}

	return &LevelDBStore{
		path: cfg.DataDirectoryPath,
		db:   db,
	}, nil
}

// Put implements the Store interface.
func (s *LevelDBStore) Put(key, value []byte) error {
	return s.db.Put(key, value, nil)
}

// Get implements the Store interface.
func (s *LevelDBStore) Get(key []byte) ([]byte, error) {
	value, err := s.db.Get(key, nil)
	if err == leveldb.ErrNotFound {
		err = ErrKeyNotFound
	}
	return value, err
}

// Delete implements the Store interface.
func (s *LevelDBStore) Delete(key []byte) error {
	return s.db.Delete(key, nil)
}

// PutBatch implements the Store interface.
func (s *LevelDBStore) PutBatch(batch Batch) error {
	lvldbBatch := batch.(*leveldb.Batch)
	err := s.db.Write(lvldbBatch, nil)
	lvldbBatch.Reset()
	s.mtx.Lock()
	s.batches = append(s.batches, lvldbBatch)
	s.mtx.Unlock()
	return err
}

// Seek implements the Store interface.
func (s *LevelDBStore) Seek(key []byte, f func(k, v []byte)) {
	iter := s.db.NewIterator(util.BytesPrefix(key), nil)
	for iter.Next() {
		f(iter.Key(), iter.Value())
	}
	iter.Release()
}

// Batch implements the Batch interface and returns a leveldb
// compatible Batch.
func (s *LevelDBStore) Batch() Batch {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	ln := len(s.batches)
	if ln == 0 {
		return new(leveldb.Batch)
	}

	b := s.batches[ln-1]
	s.batches = s.batches[:ln-1]
	return b
}

// Close implements the Store interface.
func (s *LevelDBStore) Close() error {
	return s.db.Close()
}
