package cmd

import (
	"crypto/rand"
	"fmt"
	"github.com/aravindarc/dizkv/kv"
	"github.com/aravindarc/dizkv/raft"
	"github.com/aravindarc/dizkv/rpc"
	"github.com/labstack/echo/v4"
	"github.com/spf13/cobra"
	"log"
	"math/big"
	"strconv"
)

// nrand generates random int64 number
func nrand() int64 {
	max := big.NewInt(int64(1) << 62)
	bigx, _ := rand.Int(rand.Reader, max)
	x := bigx.Int64()
	return x
}

func NewRootCmd() (cmd *cobra.Command) {
	cmd = &cobra.Command{
		Use: "dizkv",
		Run: func(cmd *cobra.Command, args []string) {
			port, err := cmd.Flags().GetInt("port")
			if err != nil {
				return
			}

			httpport, err := cmd.Flags().GetInt("httpport")
			if err != nil {
				return
			}

			nodes, err := cmd.Flags().GetInt("nodes")
			kvNodes := make([]*kv.KV, nodes)
			for i := 0; i < nodes; i++ {
				peers := make([]*rpc.Client, nodes)
				for j := 0; j < nodes; j++ {
					if j == i {
						continue
					}
					peers[j] = rpc.MakeClient("localhost:" + strconv.Itoa(port+j))
				}
				e := echo.New()
				kvNodes[i] = kv.StartKVServer(peers, i, raft.MakePersister(), 1000, e)
				i := i
				go func() {
					fmt.Println("Starting http server :" + strconv.Itoa(httpport+i))
					err := e.Start(":" + strconv.Itoa(httpport+i))
					if err != nil {
						return
					}
				}()
			}

			servers := make([]*rpc.Server, nodes)
			for i := 0; i < nodes; i++ {
				servers[i] = rpc.MakeServer()
				kvsvc := rpc.MakeService(kvNodes[i])
				rfsvc := rpc.MakeService(kvNodes[i].Rf)
				servers[i].AddService(kvsvc)
				servers[i].AddService(rfsvc)
			}

			killCh := make(chan bool)
			for i := 0; i < nodes; i++ {
				i := i
				go func() {
					err := servers[i].Start("localhost:"+strconv.Itoa(port+i), kvNodes[i].Rf, kvNodes[i])
					if err != nil {
						log.Fatalf("Error starting server %v", err)
					}
				}()
			}
			<-killCh
		},
	}

	cmd.PersistentFlags().Int("port", 7340, "port")
	cmd.PersistentFlags().Int("httpport", 9080, "httpport")
	cmd.PersistentFlags().StringSlice("peers", []string{}, "the host for peers, comma separated list")
	cmd.PersistentFlags().Int("nodes", 3, "nodes")
	cmd.PersistentFlags().Int("id", 0, "id")

	return cmd
}

func Execute() {
	c := NewRootCmd()
	c.AddCommand(NewStartCmd())
	c.AddCommand(NewJoinCmd())
	c.AddCommand(NewDemoCmd())
	err := c.Execute()
	if err != nil {
		panic(err)
	}
}
