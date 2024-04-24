package rpc

import (
	"bytes"
	"encoding/gob"
	"log"
	"net"
	"net/http"
	"net/rpc"
	"reflect"
	"strings"
	"sync"
)

// req is the client request structure
type req struct {
	method   string // Raft.AppendEntries or Raft.RequestVote or Raft.InstallSnapshot
	argsType reflect.Type
	args     []byte
}

// res is the server response structure
type res struct {
	ok       bool
	response []byte
}

// Client is the client object used by Server to send requests to other Servers
type Client struct {
	C        *rpc.Client
	endpoint string
}

func (cli *Client) GetEndpoint() string {
	return cli.endpoint
}

func MakeClient(endpoint string) *Client {
	client := &Client{
		endpoint: endpoint,
	}
	return client
}

func (cli *Client) Status() bool {
	return cli.C != nil
}

func (cli *Client) Connect() error {
	if cli.C != nil {
		cli.C.Close()
	}
	c, err := rpc.DialHTTP("tcp", cli.endpoint)
	cli.C = c
	return err
}

func (cli *Client) Call(meth string, args interface{}, response interface{}) bool {
	var r req
	r.method = meth

	err := cli.Connect()
	if err != nil {
		log.Printf("rpc.Client.Connect(): %v\n", err)
		return false
	}

	err = cli.C.Call(r.method, args, response)
	if err != nil {
		log.Printf("rpc.Client.Call(): %v\n", err)
		return false
	}

	return true
}

// Server is the rpc server that will be started in each Node of the cluster
type Server struct {
	mu       sync.Mutex
	services map[string]*Service
	count    int // incoming RPCs
}

// MakeServer creates a Server object
func MakeServer() *Server {
	rs := &Server{}
	rs.services = map[string]*Service{}
	return rs
}

// Start
func (rs *Server) Start(address string, rf interface{}, kv interface{}) error {
	err := rpc.Register(rf)
	if err != nil {
		return err
	}
	err = rpc.Register(kv)
	if err != nil {
		return err
	}
	rpc.HandleHTTP()
	l, e := net.Listen("tcp", address)
	if e != nil {
		log.Fatal("listen error:", e)
	}
	err = http.Serve(l, nil)
	if err != nil {
		return err
	}
	return nil
}

// AddService to a Server
func (rs *Server) AddService(svc *Service) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.services[svc.name] = svc
}

// dispatch request message
func (rs *Server) dispatch(req req) res {
	rs.mu.Lock()

	rs.count += 1

	// split Raft.AppendEntries into service and method
	dot := strings.LastIndex(req.method, ".")
	serviceName := req.method[:dot]
	methodName := req.method[dot+1:]

	service, ok := rs.services[serviceName]

	rs.mu.Unlock()

	if ok {
		return service.dispatch(methodName, req)
	} else {
		choices := []string{}
		for k := range rs.services {
			choices = append(choices, k)
		}
		log.Fatalf("labrpc.Server.dispatch(): unknown service %v in %v.%v; expecting one of %v\n",
			serviceName, serviceName, methodName, choices)
		return res{false, nil}
	}
}

// Service is an Object which has methods to be exposed through rpc, for example Raft and KVStore
type Service struct {
	name    string
	recv    reflect.Value
	typ     reflect.Type
	methods map[string]reflect.Method
}

// MakeService initializes a Service object
func MakeService(rcvr interface{}) *Service {
	svc := &Service{}
	svc.typ = reflect.TypeOf(rcvr)
	svc.recv = reflect.ValueOf(rcvr)
	svc.name = reflect.Indirect(svc.recv).Type().Name()
	svc.methods = map[string]reflect.Method{}

	for m := 0; m < svc.typ.NumMethod(); m++ {
		method := svc.typ.Method(m)
		mname := method.Name

		// todo check if method is valid to be used through RPC
		svc.methods[mname] = method
	}

	return svc
}

// dispatch a service
func (svc *Service) dispatch(methname string, req req) res {
	if method, ok := svc.methods[methname]; ok {
		// prepare space into which to read the argument.
		// the Value's type will be a pointer to req.argsType.
		args := reflect.New(req.argsType)

		// decode the argument.
		ab := bytes.NewBuffer(req.args)
		ad := gob.NewDecoder(ab)
		ad.Decode(args.Interface())

		// allocate space for the reply.
		replyType := method.Type.In(2)
		replyType = replyType.Elem()
		replyv := reflect.New(replyType)

		// call the method.
		function := method.Func
		function.Call([]reflect.Value{svc.recv, args.Elem(), replyv})

		// encode the reply.
		rb := new(bytes.Buffer)
		re := gob.NewEncoder(rb)
		re.EncodeValue(replyv)

		return res{true, rb.Bytes()}
	} else {
		choices := []string{}
		for k := range svc.methods {
			choices = append(choices, k)
		}
		log.Fatalf("rpc.Service.dispatch(): unknown method %v in %v; expecting one of %v\n",
			methname, req.method, choices)
		return res{false, nil}
	}
}
