package raft

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"github.com/aravindarc/dizkv/rpc"
	"github.com/labstack/echo/v4"
	"math/rand"
	"strconv"
	"sync"
	"time"
)

// States that a Server can have
const (
	FOLLOWER int = iota
	CANDIDATE
	LEADER
)

// ApplyMsg structure
// As each Raft peer becomes aware that successive logs entries are
// committed, the peer should send an ApplyMsg to the service (or
// tester) on the same server, via the applyCh passed to Make().
type ApplyMsg struct {
	Index       int
	Command     interface{}
	UseSnapshot bool
	Snapshot    []byte
}

// Log is the individual item in each server Logs
type Log struct {
	Index   int
	Term    int
	Command interface{}
}

// Peer is the Raft Peer
type Raft struct {
	mu        sync.Mutex    // Lock to protect shared access to this peer's state
	peers     []*rpc.Client // RPC end points of all peers
	persister *Persister    // Object to hold this peer's persisted state
	Me        int           // this peer's index into peers[]

	// persistent state on all servers
	// updated on stable storage before responding to RPCs
	currentTerm int   // latest term server has seen
	votedFor    int   // candidateID that received vote in current term
	logs        []Log // log

	// volatile state on all servers
	commitIndex int // index of highest log entry known to be committed
	lastApplied int // index of highest log entry applied to state machine

	// volatile state on leaders
	nextIndex  []int // for each server, index of the next log entry to send
	matchIndex []int // for each server, index of the highest log entry known to be replicated

	// extra information
	state     int
	voteCount int

	// channels
	heartBeatCh chan bool
	grantVoteCh chan bool
	winElectCh  chan bool
	commitCh    chan bool
	killCh      chan bool
	applyCh     chan ApplyMsg
}

// GetState returns currentTerm and whether this server believes it is the leader.
func (rf *Raft) GetState() (int, bool) {
	var term int
	var isLeader bool

	rf.mu.Lock()
	defer rf.mu.Unlock()

	term = rf.currentTerm
	isLeader = rf.state == LEADER
	return term, isLeader
}

// GetPersistSize returns RaftStateSize in integer
func (rf *Raft) GetPersistSize() int {
	return rf.persister.RaftStateSize()
}

// persist saves Raft's persistent state to stable storage,
// where it can later be retrieved after a crash and restart.
// see paper's Figure 2 for a description of what should be persistent.
func (rf *Raft) persist() {
	w := new(bytes.Buffer)
	e := gob.NewEncoder(w)
	e.Encode(rf.currentTerm)
	e.Encode(rf.votedFor)
	e.Encode(rf.logs)
	data := w.Bytes()
	rf.persister.SaveRaftState(data)
}

// readPersistdata restores previously persisted state.
func (rf *Raft) readPersist(data []byte) {
	if data == nil || len(data) < 1 {
		return
	}

	r := bytes.NewBuffer(data)
	d := gob.NewDecoder(r)
	d.Decode(&rf.currentTerm)
	d.Decode(&rf.votedFor)
	d.Decode(&rf.logs)
}

// InstallSnapshotArgs RPC structure
type InstallSnapshotArgs struct {
	Term             int    // leader's term
	LeaderID         int    // so follower can redirect clients
	LastIncludeIndex int    // snapshot replaces all entries up through and including this index
	LastIncludeTerm  int    // term of lastIncludedIndex
	Data             []byte // raw bytes of the snapshot chunk, starting at offset
}

// InstallSnapshotReply RPC structure
type InstallSnapshotReply struct {
	Term int // currentTerm, for leader to update itself
}

// TruncateLogs drops unnecessary logs
func (rf *Raft) TruncateLogs(lastIndex int, lastTerm int) {
	ind := -1
	first := Log{Index: lastIndex, Term: lastTerm}
	for i := len(rf.logs) - 1; i >= 0; i-- {
		if rf.logs[i].Index == lastIndex && rf.logs[i].Term == lastTerm {
			ind = i
			break
		}
	}
	if ind < 0 {
		rf.logs = []Log{first}
	} else {
		rf.logs = append([]Log{first}, rf.logs[ind+1:]...)
	}

	return
}

// readSnapshot reads a list of byte data
func (rf *Raft) readSnapshot(data []byte) {
	rf.readPersist(rf.persister.ReadRaftState())

	if len(data) == 0 {
		return
	}

	r := bytes.NewBuffer(data)
	d := gob.NewDecoder(r)
	var LastIncludedIndex int
	var LastIncludedTerm int
	d.Decode(&LastIncludedIndex)
	d.Decode(&LastIncludedTerm)

	rf.mu.Lock()
	rf.commitIndex = LastIncludedIndex
	rf.lastApplied = LastIncludedIndex
	rf.TruncateLogs(LastIncludedIndex, LastIncludedTerm)
	rf.mu.Unlock()

	msg := ApplyMsg{
		UseSnapshot: true,
		Snapshot:    data,
	}

	go func() {
		select {
		case rf.applyCh <- msg:
		case <-rf.killCh:
			return
		}
	}()
}

// InstallSnapshot RPC handler
func (rf *Raft) InstallSnapshot(args InstallSnapshotArgs, reply *InstallSnapshotReply) error {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// reply immediately if term < currentTerm
	reply.Term = rf.currentTerm
	if args.Term < rf.currentTerm {
		return nil
	}

	select {
	case rf.heartBeatCh <- true:
	case <-rf.killCh:
		return nil
	}

	if args.Term > rf.currentTerm {
		rf.currentTerm = args.Term
		rf.state = FOLLOWER
		rf.votedFor = -1
	}

	// save snapshot file, discard any existing or partial snapshot with a smaller index
	rf.persister.SaveSnapshot(args.Data)
	rf.TruncateLogs(args.LastIncludeIndex, args.LastIncludeTerm)
	rf.lastApplied = args.LastIncludeIndex
	rf.commitIndex = args.LastIncludeIndex
	rf.persist()

	msg := ApplyMsg{
		UseSnapshot: true,
		Snapshot:    args.Data,
	}

	select {
	case rf.applyCh <- msg:
	case <-rf.killCh:
		return nil
	}

	return nil
}

// sendSnapshot RPC to a server
func (rf *Raft) sendSnapshot(server int, args InstallSnapshotArgs, reply *InstallSnapshotReply) bool {
	if rf.peers[server] == nil {
		return false
	}
	ok := rf.peers[server].Call("Raft.InstallSnapshot", args, reply)

	rf.mu.Lock()
	defer rf.mu.Unlock()

	if ok {
		if reply.Term > rf.currentTerm {
			rf.currentTerm = reply.Term
			rf.state = FOLLOWER
			rf.votedFor = -1
			return false
		}
		rf.nextIndex[server] = args.LastIncludeIndex + 1
		rf.matchIndex[server] = args.LastIncludeIndex
	}

	return ok
}

// StartSnapshot with a list of by and index
func (rf *Raft) StartSnapshot(snapshot []byte, index int) error {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	firstIndex := rf.logs[0].Index
	lastIndex := rf.getLastIndex()

	if index <= firstIndex || index > lastIndex {
		return nil
	}

	first := Log{Index: index, Term: rf.logs[index-firstIndex].Term}
	rf.logs = append([]Log{first}, rf.logs[index-firstIndex+1:]...)

	rf.persist()

	w := new(bytes.Buffer)
	e := gob.NewEncoder(w)
	e.Encode(rf.logs[0].Index)
	e.Encode(rf.logs[0].Term)
	data := w.Bytes()
	data = append(data, snapshot...)

	rf.persister.SaveSnapshot(data)

	return nil
}

// AppendEntriesArgs RPC structure
type AppendEntriesArgs struct {
	Term         int   // leader's term
	LeaderID     int   // so follower can redirect clients
	PrevLogIndex int   // index of log entry immediately preceding new ones
	PrevLogTerm  int   // term of prevLogIndex entry
	Entries      []Log // log entries to store (empty for heartbeat)
	LeaderCommit int   // leader's commitIndex
}

// AppendEntriesReply RPC structure
type AppendEntriesReply struct {
	Term      int  // currentTerm, for leader to update itself
	Success   bool // true if follower contained entry matching prevLogIndex and prevLogTerm
	NextIndex int  // index to try next
}

// AppendEntries RPC handler
func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) error {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	defer rf.persist()

	// default reply is false
	// return if term < currentTerm
	// return if log doesn't contain an entry at prevLogIndex whose term matches prevLogTerm
	reply.Success = false
	reply.Term = args.Term

	if args.Term < rf.currentTerm {
		reply.Term = rf.currentTerm
		reply.NextIndex = rf.getLastIndex() + 1
		return nil
	}

	select {
	case rf.heartBeatCh <- true:
	case <-rf.killCh:
		return nil
	}

	// if argument term > current term, update current term and turn to follower
	if args.Term > rf.currentTerm {
		rf.currentTerm = args.Term
		rf.state = FOLLOWER
		rf.votedFor = -1
	}

	if args.PrevLogIndex > rf.getLastIndex() {
		reply.NextIndex = rf.getLastIndex() + 1
		return nil
	}

	firstIndex := 0
	if len(rf.logs) > 0 {
		firstIndex = rf.logs[0].Index
	}

	// if an existing entry conflicts with a new one (same index but different term)
	// delete the existing entry and all that follow it
	if args.PrevLogIndex >= firstIndex {
		term := rf.logs[args.PrevLogIndex-firstIndex].Term

		if args.PrevLogTerm != term {
			reply.NextIndex = firstIndex
			for i := args.PrevLogIndex - 1; i >= firstIndex; i-- {
				if rf.logs[i-firstIndex].Term != term {
					reply.NextIndex = i + 1
					break
				}
			}
			return nil
		}

		// append nay new entries not already in the log
		rf.logs = append(rf.logs[:args.PrevLogIndex+1-firstIndex], args.Entries...)

		reply.NextIndex = rf.getLastIndex() + 1
		reply.Success = true
	}

	// if leaderCOmmit > commitIndex, set commitIndex = min(leaderCommit, index of last new entry)
	if args.LeaderCommit > rf.commitIndex {
		last := rf.getLastIndex()

		if args.LeaderCommit > last {
			rf.commitIndex = last
		} else {
			rf.commitIndex = args.LeaderCommit
		}

		select {
		case rf.commitCh <- true:
		case <-rf.killCh:
			return nil
		}
	}

	return nil
}

// sendAppendEntries RPC to a server
func (rf *Raft) sendAppendEntries(server int, args AppendEntriesArgs, reply *AppendEntriesReply) bool {
	if rf.peers[server] == nil {
		return false
	}
	ok := rf.peers[server].Call("Raft.AppendEntries", &args, reply)

	rf.mu.Lock()
	defer rf.mu.Unlock()

	if ok {
		if rf.state != LEADER || args.Term != rf.currentTerm {
			return ok
		}

		if reply.Term > rf.currentTerm {
			rf.currentTerm = reply.Term
			rf.state = FOLLOWER
			rf.votedFor = -1
			rf.persist()
			return ok
		}

		if reply.Success {
			if len(args.Entries) > 0 {
				rf.nextIndex[server] = args.Entries[len(args.Entries)-1].Index + 1
				rf.matchIndex[server] = rf.nextIndex[server] - 1
			}
		} else {
			rf.nextIndex[server] = reply.NextIndex
		}
	}

	rf.persist()
	return ok
}

// broadcaseAppendEntries sends RPCs to all peers
func (rf *Raft) broadcastAppendEntries() {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	if rf.state == LEADER {
		firstIndex := 0
		if len(rf.logs) > 0 {
			firstIndex = rf.logs[0].Index
		}

		for i := range rf.peers {
			if i == rf.Me {
				continue
			}
			if i < len(rf.nextIndex) && rf.nextIndex[i] > firstIndex {
				var args AppendEntriesArgs
				args.Term = rf.currentTerm
				args.LeaderID = rf.Me
				args.PrevLogIndex = rf.nextIndex[i] - 1
				args.PrevLogTerm = rf.logs[args.PrevLogIndex-firstIndex].Term
				args.LeaderCommit = rf.commitIndex
				start := args.PrevLogIndex + 1 - firstIndex
				args.Entries = make([]Log, len(rf.logs[start:]))
				copy(args.Entries, rf.logs[start:])

				go func(server int, args AppendEntriesArgs) {
					reply := AppendEntriesReply{}
					rf.sendAppendEntries(server, args, &reply)
				}(i, args)
			} else {
				var args InstallSnapshotArgs
				args.Term = rf.currentTerm
				args.LeaderID = rf.Me
				args.LastIncludeIndex = rf.logs[0].Index
				args.LastIncludeTerm = rf.logs[0].Term
				args.Data = rf.persister.ReadSnapshot()

				go func(server int, args InstallSnapshotArgs) {
					reply := &InstallSnapshotReply{}
					rf.sendSnapshot(server, args, reply)
				}(i, args)
			}
		}

		// if there exists and N such at N > commitIndex,
		// a mojority of matchIndex[i] >= N and log[N].term == currentTerm,
		// then set commitIndex = N
		nextCommit := rf.commitIndex
		last := rf.getLastIndex()
		for i := nextCommit + 1; i <= last; i++ {
			count := 1
			for j := range rf.peers {
				if j != rf.Me && rf.matchIndex[j] >= i && rf.logs[i-firstIndex].Term == rf.currentTerm {
					count++
				}
			}
			if 2*count > len(rf.peers) {
				nextCommit = i
			}
		}

		if nextCommit != rf.commitIndex && rf.logs[nextCommit-firstIndex].Term == rf.currentTerm {
			rf.commitIndex = nextCommit
			select {
			case rf.commitCh <- true:
			case <-rf.killCh:
				return
			}
		}
	}
}

// RequestVoteArgs RPC structure
type RequestVoteArgs struct {
	Term         int // candidate's term
	CandidateID  int // candidate requesting vote
	LastLogIndex int // index of candidate's last log entry
	LastLogTerm  int // term of candidate's last log entry
}

// RequestVoteReply RPC structure
type RequestVoteReply struct {
	Term        int  // currentTerm, for candidate to update itself
	VoteGranted bool // true means candidate received vote
}

// RequestVote RPC handler
func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) error {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	defer rf.persist()

	// default reply is false
	// return if term < currentTerm
	reply.VoteGranted = false

	if args.Term < rf.currentTerm {
		reply.Term = rf.currentTerm
		return nil
	}

	if args.Term > rf.currentTerm {
		rf.currentTerm = args.Term
		rf.state = FOLLOWER
		rf.votedFor = -1
	}

	reply.Term = rf.currentTerm

	// if voteFor is null or candidateId,
	// and candidate's log is at least as up-to-date as receiver's log, grant vote
	if (rf.votedFor == -1 || rf.votedFor == args.CandidateID) &&
		rf.isUptoDate(args.LastLogIndex, args.LastLogTerm) {
		reply.VoteGranted = true
		rf.votedFor = args.CandidateID

		select {
		case rf.grantVoteCh <- true:
		case <-rf.killCh:
			return nil
		}

		rf.state = FOLLOWER
	}
	return nil
}

// getLastIndex returns logs last index
func (rf *Raft) getLastIndex() int {
	return rf.logs[len(rf.logs)-1].Index
}

// getLastTerm returns last index's term
func (rf *Raft) getLastTerm() int {
	return rf.logs[len(rf.logs)-1].Term
}

// isUptoDate checks if term is up-to-date
func (rf *Raft) isUptoDate(candIndex int, candTerm int) bool {
	term, index := rf.getLastTerm(), rf.getLastIndex()

	if candTerm != term {
		return candTerm > term
	}

	return candIndex >= index
}

// example code to send a RequestVote RPC to a server.
// server is the index of the target server in rf.peers[].
// expects RPC arguments in args.
// fills in *reply with RPC reply, so caller should
// pass &reply.
// the types of the args and reply passed to Call() must be
// the same as the types of the arguments declared in the
// handler function (including whether they are pointers).
//
// The labrpc package simulates a lossy network, in which servers
// may be unreachable, and in which requests and replies may be lost.
// Call() sends a request and waits for a reply. If a reply arrives
// within a timeout interval, Call() returns true; otherwise
// Call() returns false. Thus Call() may not return for a while.
// A false return can be caused by a dead server, a live server that
// can't be reached, a lost request, or a lost reply.
//
// Call() is guaranteed to return (perhaps after a delay) *except* if the
// handler function on the server side does not return.  Thus there
// is no need to implement your own timeouts around Call().
//
// look at the comments in ../labrpc/labrpc.go for more details.
//
// if you're having trouble getting RPC to work, check that you've
// capitalized all field names in structs passed over RPC, and
// that the caller passes the address of the reply struct with &, not
// the struct itself.
func (rf *Raft) sendRequestVote(server int, args *RequestVoteArgs, reply *RequestVoteReply) bool {
	if rf.peers[server] == nil {
		return false
	}
	ok := rf.peers[server].Call("Raft.RequestVote", args, reply)
	rf.mu.Lock()
	defer rf.mu.Unlock()

	if ok {
		if rf.state != CANDIDATE {
			return ok
		}

		term := rf.currentTerm
		if args.Term != term {
			return ok
		}

		if reply.Term > term {
			rf.currentTerm = reply.Term
			rf.state = FOLLOWER
			rf.votedFor = -1
			rf.persist()
		}

		// if receive majority of votes, become a leader
		if reply.VoteGranted {
			rf.voteCount++
			if rf.state == CANDIDATE && 2*rf.voteCount > len(rf.peers) {
				rf.state = LEADER
				rf.winElectCh <- true
			}
		}
	}

	return ok
}

// broadcastRequestVote to all peers
func (rf *Raft) broadcastRequestVote() {
	var args RequestVoteArgs
	rf.mu.Lock()

	args.Term = rf.currentTerm
	args.CandidateID = rf.Me
	args.LastLogIndex = rf.getLastIndex()
	args.LastLogTerm = rf.getLastTerm()
	numPeers := len(rf.peers)
	me := rf.Me
	rf.mu.Unlock()

	for i := 0; i < numPeers; i++ {
		rf.mu.Lock()
		if rf.state != CANDIDATE {
			break
		}
		rf.mu.Unlock()

		if i == me {
			continue
		}

		go func(server int) {
			reply := RequestVoteReply{}
			rf.sendRequestVote(server, &args, &reply)
		}(i)
	}
}

// Start the service using Raft (e.g. a k/v server) wants to start
// agreement on the next command to be appended to Raft's logs. if this
// server isn't the leader, returns false. otherwise start the
// agreement and return immediately. there is no guarantee that this
// command will ever be committed to the Raft logs, since the leader
// may fail or lose an election.
//
// the first return value is the index that the command will appear at
// if it's ever committed. the second return value is the current
// term. the third return value is true if this server believes it is
// the leader.
func (rf *Raft) Start(command interface{}) (int, int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	index := -1
	term := rf.currentTerm
	isLeader := rf.state == LEADER

	if isLeader {
		index = rf.getLastIndex() + 1
		rf.logs = append(rf.logs, Log{Index: index, Term: term, Command: command})
		rf.persist()
	}

	return index, term, isLeader
}

// Kill is called by the tester calls when a Raft instance won't
// be needed again. you are not required to do anything
// in Kill(), but it might be convenient to (for example)
// turn off debug output from this instance.
func (rf *Raft) Kill() {
	// Your code here, if desired.
	close(rf.killCh)
}

// Make a Raft instance
// the service or tester wants to create a Raft server. the ports
// of all the Raft servers (including this one) are in peers[]. this
// server's port is peers[Me]. all the servers' peers[] arrays
// have the same order. persister is a place for this server to
// save its persistent state, and also initially holds the most
// recent saved state, if any. applyCh is a channel on which the
// tester or service expects Raft to send ApplyMsg messages.
// Make() must return quickly, so it should start goroutines
// for any long-running work.
func Make(peers []*rpc.Client, me int,
	persister *Persister, applyCh chan ApplyMsg, e *echo.Echo) *Raft {
	rf := &Raft{}

	rf.mu.Lock()
	rf.peers = peers
	rf.persister = persister
	rf.Me = me

	rf.votedFor = -1
	rf.currentTerm = 0
	rf.state = FOLLOWER
	rf.commitIndex = 0
	rf.lastApplied = 0
	rf.logs = append(rf.logs, Log{Term: 0})

	rf.heartBeatCh = make(chan bool, 100)
	rf.grantVoteCh = make(chan bool, 100)
	rf.winElectCh = make(chan bool, 100)
	rf.commitCh = make(chan bool, 100)
	rf.killCh = make(chan bool)
	rf.applyCh = applyCh
	rf.mu.Unlock()

	rf.readPersist(persister.ReadRaftState())
	rf.readSnapshot(persister.ReadSnapshot())

	e.GET("/nodecount", func(c echo.Context) error {
		rf.mu.Lock()
		defer rf.mu.Unlock()
		var count int
		for _, peer := range rf.peers {
			if peer != nil {
				count++
			}
		}
		fmt.Println("count", count)
		return c.String(200, strconv.Itoa(count))
	})

	e.GET("/details", func(c echo.Context) error {
		return c.JSON(200, struct {
			State int
			Logs  []Log
		}{
			State: rf.state,
			Logs:  rf.logs,
		})
	})

	go rf.run()

	go rf.commitLogs()

	return rf
}

// run kicks off Leader Election process
func (rf *Raft) run() {
	for {
		rf.mu.Lock()
		currState := rf.state
		rf.mu.Unlock()
		switch currState {
		case FOLLOWER:
			select {
			case <-rf.heartBeatCh:
			case <-rf.grantVoteCh:
			case <-rf.killCh:
				return
			case <-time.After(time.Millisecond * time.Duration(rand.Intn(200)+300)):
				rf.mu.Lock()
				rf.state = CANDIDATE
				rf.mu.Unlock()
			}
		case CANDIDATE:
			rf.mu.Lock()
			rf.currentTerm++
			rf.votedFor = rf.Me
			rf.voteCount = 1
			rf.persist()
			rf.mu.Unlock()
			go rf.broadcastRequestVote()
			select {
			case <-rf.heartBeatCh:
				rf.mu.Lock()
				rf.state = FOLLOWER
				rf.mu.Unlock()
			case <-rf.winElectCh:
				rf.mu.Lock()
				rf.state = LEADER
				rf.nextIndex = make([]int, len(rf.peers))
				rf.matchIndex = make([]int, len(rf.peers))
				for i := range rf.peers {
					rf.nextIndex[i] = rf.getLastIndex() + 1
					rf.matchIndex[i] = 0
				}
				rf.mu.Unlock()
			case <-rf.killCh:
				return
			case <-time.After(time.Millisecond * time.Duration(rand.Intn(200)+300)):
			}
		case LEADER:
			go rf.broadcastAppendEntries()
			time.Sleep(time.Millisecond * 100)
		}
	}
}

// commitLogs kicks off Log Replication process
func (rf *Raft) commitLogs() {
	for {
		select {
		case <-rf.commitCh:
			rf.mu.Lock()
			commitIndex := rf.commitIndex

			firstIndex := 0
			if len(rf.logs) > 0 {
				firstIndex = rf.logs[0].Index
			}
			for i := rf.lastApplied + 1; i <= commitIndex; i++ {
				msg := ApplyMsg{Index: i, Command: rf.logs[i-firstIndex].Command}
				select {
				case rf.applyCh <- msg:
				case <-rf.killCh:
					return
				}
				rf.lastApplied = i
			}
			rf.persist()
			rf.mu.Unlock()
		case <-rf.killCh:
			return
		}
	}
}

// AddPeerArgs RPC structure
type AddPeerArgs struct {
	Endpoint string
}

// AddPeerReply RPC structure
type AddPeerReply struct {
	Success bool
}

// AddPeer RPC handler
func (rf *Raft) AddPeer(args *AddPeerArgs, reply *AddPeerReply) error {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	// check if endpoint already exists
	for _, peer := range rf.peers {
		if peer != nil && peer.GetEndpoint() == args.Endpoint {
			return nil
		}
	}

	rf.peers = append(rf.peers, rpc.MakeClient(args.Endpoint))
	reply.Success = true

	return nil
}
