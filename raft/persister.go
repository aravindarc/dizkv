package raft

import "sync"

// Persister structure
type Persister struct {
	mu        sync.Mutex
	raftstate []byte
	snapshot  []byte
}

// MakePersister create a Persister instance
func MakePersister() *Persister {
	return &Persister{}
}

// Copy a Persister
func (ps *Persister) Copy() *Persister {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	np := MakePersister()
	np.raftstate = ps.raftstate
	np.snapshot = ps.snapshot
	return np
}

// SaveRaftState save data in a list of byte
func (ps *Persister) SaveRaftState(data []byte) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.raftstate = data
}

// ReadRaftState return a list of byte
func (ps *Persister) ReadRaftState() []byte {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.raftstate
}

// RaftStateSize return state size in int
func (ps *Persister) RaftStateSize() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return len(ps.raftstate)
}

// SaveSnapshot save a snapshot data in a list of byte
func (ps *Persister) SaveSnapshot(snapshot []byte) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.snapshot = snapshot
}

// ReadSnapshot read data in list of byte
func (ps *Persister) ReadSnapshot() []byte {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.snapshot
}

// SnapshotSize return the value in int
func (ps *Persister) SnapshotSize() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return len(ps.snapshot)
}
