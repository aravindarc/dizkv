package kv

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"github.com/aravindarc/dizkv/raft"
	"github.com/aravindarc/dizkv/rpc"
	"github.com/labstack/echo/v4"
	"sync"
	"time"
)

// Op structure
type Op struct {
	Type      string
	Key       string
	Value     string
	ClientID  int64
	RequestID int
}

// KV structure that holds Raft instance
type KV struct {
	mu      sync.Mutex
	me      int
	Rf      *raft.Raft
	applyCh chan raft.ApplyMsg
	leader  int

	maxraftstate int // snapshot if log grows this big

	kvDB   map[string]string
	dup    map[int64]int
	result map[int]chan Op
	killCh chan bool
}

// AppendEntry appends an entry Op and returns a boolean
func (kv *KV) AppendEntry(entry Op) bool {
	index, _, isLeader := kv.Rf.Start(entry)
	if !isLeader {
		return false
	}

	kv.mu.Lock()
	ch, ok := kv.result[index]

	if !ok {
		ch = make(chan Op, 1)
		kv.result[index] = ch
	}
	kv.mu.Unlock()

	select {
	case op := <-ch:
		return op.ClientID == entry.ClientID && op.RequestID == entry.RequestID
	case <-time.After(800 * time.Millisecond):
		return false
	}
	return false
}

// GetArgs structure for Get Argument
type GetArgs struct {
	Key       string
	ClientID  int64
	RequestID int
}

// GetReply structure for Get Reply
type GetReply struct {
	WrongLeader bool
	Err         string
	Value       string
}

// Get RPC
func (kv *KV) Get(args *GetArgs, reply *GetReply) error {
	entry := Op{Type: "Get", Key: args.Key, ClientID: args.ClientID, RequestID: args.RequestID}

	ok := kv.AppendEntry(entry)
	if !ok {
		reply.WrongLeader = true
	} else {
		reply.WrongLeader = false
		reply.Err = "OK"

		kv.mu.Lock()
		reply.Value, ok = kv.kvDB[args.Key]
		if !ok {
			reply.Value = ""
		}
		kv.dup[args.ClientID] = args.RequestID
		kv.mu.Unlock()
	}

	return nil
}

// PutAppendArgs structure for Put or Append Argument
type PutAppendArgs struct {
	Key       string
	Value     string
	Op        string
	ClientID  int64
	RequestID int
}

// PutAppendReply structure for Put or Append Reply
type PutAppendReply struct {
	WrongLeader bool
	Err         string
}

// PutAppend RPC
func (kv *KV) PutAppend(args *PutAppendArgs, reply *PutAppendReply) error {
	entry := Op{
		Type:      args.Op,
		Key:       args.Key,
		Value:     args.Value,
		ClientID:  args.ClientID,
		RequestID: args.RequestID,
	}

	ok := kv.AppendEntry(entry)
	if !ok {
		reply.WrongLeader = true

	} else {
		reply.WrongLeader = false
		reply.Err = "OK"
	}

	return nil
}

// AddPeerArgs RPC structure
type AddPeerArgs struct {
	Endpoint string
}

// AddPeerReply RPC structure
type AddPeerReply struct {
	WrongLeader bool
}

func (kv *KV) AddPeer(args *AddPeerArgs, reply *AddPeerReply) error {
	fmt.Println("Inside AddPeer")
	entry := Op{
		Type:      "AddPeer",
		Value:     args.Endpoint,
		ClientID:  0,
		RequestID: 0,
	}

	ok := kv.AppendEntry(entry)
	if !ok {
		reply.WrongLeader = true
	} else {
		reply.WrongLeader = false
	}

	return nil
}

// Kill is called by the tester when a RaftKV instance won't
// be needed again. you are not required to do anything
// in Kill, but it might be convenient to (for example)
// turn off debug output from this instance.
func (kv *KV) Kill() {
	kv.Rf.Kill()
	close(kv.killCh)
}

type AddServerReq struct {
	Endpoint string
}

// StartKVServer return a RaftKV
// servers[] contains the ports of the set of
// servers that will cooperate via Raft to
// form the fault-tolerant key/value service.
// me is the index of the current server in servers[].
// the k/v server should store snapshots with persister.SaveSnapshot(),
// and Raft should save its state (including log) with persister.SaveRaftState().
// the k/v server should snapshot when Raft's saved state exceeds maxraftstate bytes,
// in order to allow Raft to garbage-collect its log. if maxraftstate is -1,
// you don't need to snapshot.
// StartKVServer() must return quickly, so it should start goroutines
// for any long-running work.
func StartKVServer(servers []*rpc.Client, me int, persister *raft.Persister, maxraftstate int, e *echo.Echo) *KV {
	// call gob.Register on structures you want
	// Go's RPC library to marshall/unmarshall.
	gob.Register(Op{})

	kv := new(KV)
	kv.me = me
	kv.maxraftstate = maxraftstate

	kv.applyCh = make(chan raft.ApplyMsg, 100)
	kv.Rf = raft.Make(servers, me, persister, kv.applyCh, e)
	kv.kvDB = make(map[string]string)
	kv.result = make(map[int]chan Op)
	kv.dup = make(map[int64]int)
	kv.killCh = make(chan bool)

	e.PUT("/kv/add", func(c echo.Context) error {
		req := new(AddServerReq)
		err := c.Bind(req)
		if err != nil {
			return err
		}
		args := AddPeerArgs{Endpoint: req.Endpoint}
		for {
			reply := AddPeerReply{}
			if servers[kv.leader] == nil {
				err = kv.AddPeer(&args, &reply)
				if err != nil {
					return err
				}
				if reply.WrongLeader == false {
					return c.JSON(200, "OK")
				}
			} else {
				if servers[kv.leader].C == nil {
					err := servers[kv.leader].Connect()
					if err != nil {
						return err
					}
				}
				ok := servers[kv.leader].Call("KV.AddPeer", &args, &reply)
				if ok && reply.WrongLeader == false {
					return c.JSON(200, "OK")
				}
			}
			kv.mu.Lock()
			kv.leader = (kv.leader + 1) % len(servers)
			kv.mu.Unlock()
		}
	})

	type putRequest struct {
		Value string
	}

	e.PUT("/kv/:key", func(c echo.Context) error {
		key := c.Param("key")
		req := new(putRequest)
		err := c.Bind(req)
		if err != nil {
			return err
		}
		fmt.Println("Received PUT request for key: ", key, " with value: ", req.Value)
		args := PutAppendArgs{Key: key, Value: req.Value, Op: "Put"}
		for {
			reply := PutAppendReply{}
			if servers[kv.leader] == nil {
				err = kv.PutAppend(&args, &reply)
				if err != nil {
					return err
				}
				if reply.WrongLeader == false {
					return c.JSON(200, "OK")
				}
			} else {
				if servers[kv.leader].C == nil {
					err := servers[kv.leader].Connect()
					if err != nil {
						return err
					}
				}
				ok := servers[kv.leader].Call("KV.PutAppend", &args, &reply)
				if ok && reply.WrongLeader == false {
					return c.JSON(200, "OK")
				}
			}
			kv.mu.Lock()
			kv.leader = (kv.leader + 1) % len(servers)
			kv.mu.Unlock()
		}
	})

	e.GET("/kv/:key", func(c echo.Context) error {
		key := c.Param("key")
		args := GetArgs{Key: key}
		for {
			reply := GetReply{}
			if servers[kv.leader] == nil {
				err := kv.Get(&args, &reply)
				if err != nil {
					return err
				}
				if reply.WrongLeader == false {
					return c.JSON(200, reply.Value)
				}
			} else {
				if servers[kv.leader].C == nil {
					err := servers[kv.leader].Connect()
					if err != nil {
						return err
					}
				}
				ok := servers[kv.leader].Call("KV.Get", &args, &reply)
				if ok && reply.WrongLeader == false {
					return c.JSON(200, reply.Value)
				}
			}
			kv.mu.Lock()
			kv.leader = (kv.leader + 1) % len(servers)
			kv.mu.Unlock()
		}
	})

	e.GET("/kv", func(c echo.Context) error {
		return c.JSON(200, kv.kvDB)
	})

	go kv.run()

	return kv
}

// Run RaftKV
func (kv *KV) run() {
	for {
		select {
		case msg := <-kv.applyCh:
			if msg.UseSnapshot {
				var LastIncludedIndex int
				var LastIncludedTerm int
				r := bytes.NewBuffer(msg.Snapshot)
				d := gob.NewDecoder(r)
				kv.mu.Lock()
				d.Decode(&LastIncludedIndex)
				d.Decode(&LastIncludedTerm)
				kv.kvDB = make(map[string]string)
				kv.dup = make(map[int64]int)
				d.Decode(&kv.kvDB)
				d.Decode(&kv.dup)
				kv.mu.Unlock()
			} else {
				index := msg.Index
				op := msg.Command.(Op)
				kv.mu.Lock()
				switch op.Type {
				case "Put":
					kv.kvDB[op.Key] = op.Value
				case "Append":
					kv.kvDB[op.Key] += op.Value
				case "AddPeer":
					fmt.Println("KV in Adding peer: ", op.Value)
					args := raft.AddPeerArgs{Endpoint: op.Value}
					reply := raft.AddPeerReply{}
					_ = kv.Rf.AddPeer(&args, &reply)
				}
				kv.dup[op.ClientID] = op.RequestID
				ch, ok := kv.result[index]
				if ok {
					select {
					case <-kv.result[index]:
					case <-kv.killCh:
						return
					default:
					}
					ch <- op
				} else {
					kv.result[index] = make(chan Op, 1)
				}
				if kv.maxraftstate != -1 && kv.Rf.GetPersistSize() > kv.maxraftstate {
					w := new(bytes.Buffer)
					e := gob.NewEncoder(w)
					e.Encode(kv.kvDB)
					e.Encode(kv.dup)
					data := w.Bytes()
					go kv.Rf.StartSnapshot(data, msg.Index)
				}
				kv.mu.Unlock()
			}
		case <-kv.killCh:
			return
		}
	}
}

// Check duplication
func (kv *KV) isDup(op *Op) bool {
	v, ok := kv.dup[op.ClientID]
	if ok {
		return v >= op.RequestID
	}

	return false
}
