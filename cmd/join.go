package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/aravindarc/dizkv/kv"
	"github.com/aravindarc/dizkv/raft"
	"github.com/aravindarc/dizkv/rpc"
	"github.com/labstack/echo/v4"
	"github.com/spf13/cobra"
	"net/http"
	"strconv"
	"strings"
)

func NewJoinCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "join",
		Run: func(cmd *cobra.Command, args []string) {

			cluster, err := cmd.Flags().GetString("cluster")
			if err != nil {
				panic(err)
				return
			}

			port, err := cmd.Flags().GetInt("port")
			if err != nil {
				panic(err)
			}

			httpport, err := cmd.Flags().GetInt("httpport")
			if err != nil {
				panic(err)
			}

			peershost, err := cmd.Flags().GetStringSlice("peers")
			if err != nil {
				panic(err)
			}

			thisnode, err := cmd.Flags().GetString("thisnode")
			if err != nil {
				panic(err)
			}

			putBody, _ := json.Marshal(kv.AddServerReq{
				Endpoint: thisnode,
			})

			r, err := http.NewRequest("PUT", cluster+"/kv/add", bytes.NewBuffer(putBody))
			if err != nil {
				panic(err)
				return
			}
			r.Header.Set("Content-Type", "application/json")
			client := &http.Client{}
			_, err = client.Do(r)
			if err != nil {
				panic(err)
			}

			id, err := cmd.Flags().GetInt("id")
			if err != nil {
				panic(err)
			}

			kvNode := new(kv.KV)
			peers := make([]*rpc.Client, len(peershost))

			for _, p := range peershost {
				if !strings.Contains(p, ":") {
					p = p + ":" + "80"
				}
				peers = append(peers, rpc.MakeClient(p))
			}

			e := echo.New()
			kvNode = kv.StartKVServer(peers, id, raft.MakePersister(), 1000, e)
			go func() {
				fmt.Println("Starting http server :" + strconv.Itoa(httpport))
				err := e.Start(":" + strconv.Itoa(httpport))
				if err != nil {
					panic(err)
					return
				}
			}()

			server := new(rpc.Server)
			server = rpc.MakeServer()
			kvsvc := rpc.MakeService(kvNode)
			rfsvc := rpc.MakeService(kvNode.Rf)
			server.AddService(kvsvc)
			server.AddService(rfsvc)

			server.Start(":"+strconv.Itoa(port), kvNode.Rf, kvNode)
		},
	}

	cmd.Flags().String("cluster", "localhost:9080", "cluster address")
	cmd.Flags().String("thisnode", "", "this node address")

	return cmd
}
