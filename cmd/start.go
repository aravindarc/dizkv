package cmd

import (
	"fmt"
	"github.com/aravindarc/dizkv/kv"
	"github.com/aravindarc/dizkv/raft"
	"github.com/aravindarc/dizkv/rpc"
	"github.com/labstack/echo/v4"
	"github.com/spf13/cobra"
	"strconv"
	"strings"
)

func NewStartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "start",
		Run: func(cmd *cobra.Command, args []string) {
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

	return cmd
}
