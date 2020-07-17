package dao

import (
	"bytes"
	"errors"

	"github.com/nspcc-dev/neo-go/pkg/core/state"
	"github.com/nspcc-dev/neo-go/pkg/core/storage"
	"github.com/nspcc-dev/neo-go/pkg/io"
	"github.com/nspcc-dev/neo-go/pkg/util"
)

// Cached is a data access object that mimics DAO, but has a write cache
// for accounts and read cache for contracts. These are the most frequently used
// objects in the storeBlock().
type Cached struct {
	DAO
	accounts  map[util.Uint160]*state.Account
	contracts map[util.Uint160]*state.Contract
	balances  map[util.Uint160]*state.NEP5Balances
	transfers map[util.Uint160]map[uint32]*state.NEP5TransferLog

	dropNEP5Cache bool
}

// NewCached returns new Cached wrapping around given backing store.
func NewCached(d DAO) *Cached {
	accs := make(map[util.Uint160]*state.Account)
	ctrs := make(map[util.Uint160]*state.Contract)
	balances := make(map[util.Uint160]*state.NEP5Balances)
	transfers := make(map[util.Uint160]map[uint32]*state.NEP5TransferLog)
	return &Cached{d.GetWrapped(), accs, ctrs, balances, transfers, false}
}

// GetAccountStateOrNew retrieves Account from cache or underlying store
// or creates a new one if it doesn't exist.
func (cd *Cached) GetAccountStateOrNew(hash util.Uint160) (*state.Account, error) {
	if cd.accounts[hash] != nil {
		return cd.accounts[hash], nil
	}
	return cd.DAO.GetAccountStateOrNew(hash)
}

// GetAccountState retrieves Account from cache or underlying store.
func (cd *Cached) GetAccountState(hash util.Uint160) (*state.Account, error) {
	if cd.accounts[hash] != nil {
		return cd.accounts[hash], nil
	}
	return cd.DAO.GetAccountState(hash)
}

// PutAccountState saves given Account in the cache.
func (cd *Cached) PutAccountState(as *state.Account) error {
	cd.accounts[as.ScriptHash] = as
	return nil
}

// GetContractState returns contract state from cache or underlying store.
func (cd *Cached) GetContractState(hash util.Uint160) (*state.Contract, error) {
	if cd.contracts[hash] != nil {
		return cd.contracts[hash], nil
	}
	cs, err := cd.DAO.GetContractState(hash)
	if err == nil {
		cd.contracts[hash] = cs
	}
	return cs, err
}

// PutContractState puts given contract state into the given store.
func (cd *Cached) PutContractState(cs *state.Contract) error {
	cd.contracts[cs.ScriptHash()] = cs
	return cd.DAO.PutContractState(cs)
}

// DeleteContractState deletes given contract state in cache and backing store.
func (cd *Cached) DeleteContractState(hash util.Uint160) error {
	cd.contracts[hash] = nil
	return cd.DAO.DeleteContractState(hash)
}

// GetNEP5Balances retrieves NEP5Balances for the acc.
func (cd *Cached) GetNEP5Balances(acc util.Uint160) (*state.NEP5Balances, error) {
	if bs := cd.balances[acc]; bs != nil {
		return bs, nil
	}
	return cd.DAO.GetNEP5Balances(acc)
}

// PutNEP5Balances saves NEP5Balances for the acc.
func (cd *Cached) PutNEP5Balances(acc util.Uint160, bs *state.NEP5Balances) error {
	cd.balances[acc] = bs
	return nil
}

// GetNEP5TransferLog retrieves NEP5TransferLog for the acc.
func (cd *Cached) GetNEP5TransferLog(acc util.Uint160, index uint32) (*state.NEP5TransferLog, error) {
	ts := cd.transfers[acc]
	if ts != nil && ts[index] != nil {
		return ts[index], nil
	}
	return cd.DAO.GetNEP5TransferLog(acc, index)
}

// PutNEP5TransferLog saves NEP5TransferLog for the acc.
func (cd *Cached) PutNEP5TransferLog(acc util.Uint160, index uint32, bs *state.NEP5TransferLog) error {
	ts := cd.transfers[acc]
	if ts == nil {
		ts = make(map[uint32]*state.NEP5TransferLog, 2)
		cd.transfers[acc] = ts
	}
	ts[index] = bs
	return nil
}

// AppendNEP5Transfer appends new transfer to a transfer event log.
func (cd *Cached) AppendNEP5Transfer(acc util.Uint160, index uint32, tr *state.NEP5Transfer) (bool, error) {
	lg, err := cd.GetNEP5TransferLog(acc, index)
	if err != nil {
		return false, err
	}
	if err := lg.Append(tr); err != nil {
		return false, err
	}
	return lg.Size() >= nep5TransferBatchSize, cd.PutNEP5TransferLog(acc, index, lg)
}

// MigrateNEP5Balances migrates NEP5 balances from old contract to the new one.
func (cd *Cached) MigrateNEP5Balances(from, to util.Uint160) error {
	var (
		simpleDAO *Simple
		cachedDAO = cd
		ok        bool
		w         = io.NewBufBinWriter()
	)
	for simpleDAO == nil {
		simpleDAO, ok = cachedDAO.DAO.(*Simple)
		if !ok {
			cachedDAO, ok = cachedDAO.DAO.(*Cached)
			if !ok {
				panic("uknown DAO")
			}
		}
	}
	for acc, bs := range cd.balances {
		err := simpleDAO.putNEP5Balances(acc, bs, w)
		if err != nil {
			return err
		}
		w.Reset()
	}
	cd.dropNEP5Cache = true
	var store = simpleDAO.Store
	// Create another layer of cache because we can't change original storage
	// while seeking.
	var upStore = storage.NewMemCachedStore(store)
	store.Seek([]byte{byte(storage.STNEP5Balances)}, func(k, v []byte) {
		if !bytes.Contains(v, from[:]) {
			return
		}
		bs := state.NewNEP5Balances()
		reader := io.NewBinReaderFromBuf(v)
		bs.DecodeBinary(reader)
		if reader.Err != nil {
			panic("bad nep5 balances")
		}
		tr, ok := bs.Trackers[from]
		if !ok {
			return
		}
		delete(bs.Trackers, from)
		bs.Trackers[to] = tr
		w.Reset()
		bs.EncodeBinary(w.BinWriter)
		if w.Err != nil {
			panic("error on nep5 balance encoding")
		}
		err := upStore.Put(k, w.Bytes())
		if err != nil {
			panic("can't put value in the DB")
		}
	})
	_, err := upStore.Persist()
	return err
}

// Persist flushes all the changes made into the (supposedly) persistent
// underlying store.
func (cd *Cached) Persist() (int, error) {
	lowerCache, ok := cd.DAO.(*Cached)
	// If the lower DAO is Cached, we only need to flush the MemCached DB.
	// This actually breaks DAO interface incapsulation, but for our current
	// usage scenario it should be good enough if cd doesn't modify object
	// caches (accounts/contracts/etc) in any way.
	if ok {
		if cd.dropNEP5Cache {
			lowerCache.balances = make(map[util.Uint160]*state.NEP5Balances)
		}
		var simpleCache *Simple
		for simpleCache == nil {
			simpleCache, ok = lowerCache.DAO.(*Simple)
			if !ok {
				lowerCache, ok = cd.DAO.(*Cached)
				if !ok {
					return 0, errors.New("unsupported lower DAO")
				}
			}
		}
		return simpleCache.Persist()
	}
	buf := io.NewBufBinWriter()

	for sc := range cd.accounts {
		err := cd.DAO.putAccountState(cd.accounts[sc], buf)
		if err != nil {
			return 0, err
		}
		buf.Reset()
	}
	for acc, bs := range cd.balances {
		err := cd.DAO.putNEP5Balances(acc, bs, buf)
		if err != nil {
			return 0, err
		}
		buf.Reset()
	}
	for acc, ts := range cd.transfers {
		for ind, lg := range ts {
			err := cd.DAO.PutNEP5TransferLog(acc, ind, lg)
			if err != nil {
				return 0, err
			}
		}
	}
	return cd.DAO.Persist()
}

// GetWrapped implements DAO interface.
func (cd *Cached) GetWrapped() DAO {
	return &Cached{cd.DAO.GetWrapped(),
		cd.accounts,
		cd.contracts,
		cd.balances,
		cd.transfers,
		false,
	}
}
