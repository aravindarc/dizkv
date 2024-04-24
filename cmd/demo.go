package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/spf13/cobra"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"text/template"
)

const manifest = `apiVersion: v1
kind: Service
metadata:
  name: diskv-svc-{{ .Id }}
spec:
  selector:
    app.kubernetes.io/name: diskv-{{ .Id }}
  ports:
    - protocol: TCP
      port: 80
      targetPort: 7340
      name: rpc
    - protocol: TCP
      port: 9080
      targetPort: 9080
      name: http

---

apiVersion: v1
kind: Pod
metadata:
  labels:
    app: diskv
    app.kubernetes.io/name: diskv-{{ .Id }}
  name: diskv-{{ .Id }}
spec:
  containers:
    - command:
        - /app/dizkv
      args:
        - join
        - --id={{ .Id }}
        - --peers={{ .Peers }}
        - --thisnode=diskv-svc-{{ .Id }}:80
        - --cluster=http://diskv-svc-0:9080
      image: aravindarc/dizkv:1.22
      name: default
      resources: {}
      imagePullPolicy: Always
  dnsPolicy: ClusterFirst
  restartPolicy: Always
status: {}
`

// Demo struct to print template
type Demo struct {
	Id    string
	Peers string
}

func NewDemoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "demo",
		Run: func(cmd *cobra.Command, args []string) {
			e := echo.New()

			e.Use(middleware.CORS())

			kvhost, err := cmd.Flags().GetString("kvhost")
			if err != nil {
				panic(err)
			}

			// kvhost is of the form http://localhost:9080
			// separate the hostname and port
			// : occurs twice in the string
			port, err := strconv.Atoi(kvhost[strings.LastIndex(kvhost, ":")+1:])

			in, err := cmd.Flags().GetString("int")
			if err != nil {
				return
			}

			incp, err := cmd.Flags().GetBool("incp")
			if err != nil {
				return
			}

			e.PUT("/kv/:key", func(c echo.Context) error {
				key := c.Param("key")
				req := c.Request().Body
				if err != nil {
					return err
				}

				b, err := io.ReadAll(req)
				if err != nil {
					return err
				}

				fmt.Println("Demo Received PUT request for key: ", key, " with value: ", string(b))

				// make put request write req to kvhost
				r, err := http.NewRequest("PUT", kvhost+"/kv/"+key, bytes.NewBuffer(b))
				if err != nil {
					return err
				}
				r.Header.Set("Content-Type", "application/json")
				client := &http.Client{}
				_, err = client.Do(r)
				if err != nil {
					// handle error
				}

				return c.JSON(200, "OK")
			})

			e.GET("/nodecount", func(c echo.Context) error {
				res, err := http.Get(kvhost + "/nodecount")
				if err != nil {
					return err
				}
				var body []byte
				_, err = res.Body.Read(body)
				if err != nil {
					return err
				}
				count, err := strconv.Atoi(string(body))

				count += 1

				if err != nil {
					return err
				}
				return c.JSON(200, count)
			})

			e.GET("/state", func(c echo.Context) error {
				var client http.Client
				res, err := client.Get(kvhost + "/nodecount")
				if err != nil || res.StatusCode != 200 {
					panic(err)
					return err
				}
				body, err := io.ReadAll(res.Body)
				if err != nil {
					panic(err)
					return err
				}
				count, err := strconv.Atoi(string(body))
				if err != nil {
					panic(err)
					return err
				}

				count += 1

				kvstate := make([]map[string]string, count)

				for i := 0; i < count; i++ {
					var temp string
					if incp {
						temp = fmt.Sprintf(in, strconv.Itoa(port+i))
					} else {
						temp = fmt.Sprintf(in, strconv.Itoa(i))
					}
					if err != nil {
						return err
					}

					res, err := client.Get(temp + "/kv")
					if err != nil {
						panic(err)
						return err
					}
					var body []byte
					body, err = io.ReadAll(res.Body)
					if err != nil {
						fmt.Println("Error reading body", err)
						panic(err)
						return err
					}
					state := make(map[string]string)
					err = json.Unmarshal(body, &state)
					if err != nil {
						panic(err)
						return err
					}
					// delete keys with empty string values
					for k, v := range state {
						if v == "" {
							delete(state, k)
						}
					}
					kvstate[i] = state
				}

				return c.JSON(200, kvstate)
			})

			e.PUT("/kv/add", func(c echo.Context) error {
				var client http.Client
				res, err := client.Get(kvhost + "/nodecount")
				if err != nil || res.StatusCode != 200 {
					panic(err)
					return err
				}
				body, err := io.ReadAll(res.Body)
				if err != nil {
					panic(err)
					return err
				}
				count, err := strconv.Atoi(string(body))
				if err != nil {
					panic(err)
					return err
				}

				count += 1

				var demo Demo
				demo.Id = strconv.Itoa(count)
				peers := make([]string, count)
				for i := 0; i < count; i++ {
					peers[i] = "diskv-svc-" + strconv.Itoa(i)
				}

				demo.Peers = strings.Join(peers, ",")

				// use go template to render the manifest

				tmpl, err := template.New("demo").Parse(manifest)
				if err != nil {
					panic(err)
					return err
				}

				var buf bytes.Buffer
				err = tmpl.Execute(&buf, demo)
				if err != nil {
					panic(err)
					return err
				}

				// save the manifest to a file
				// use kubectl to apply the manifest
				err = os.WriteFile("manifest.yaml", buf.Bytes(), 0644)
				if err != nil {
					panic(err)
					return err
				}

				a := "kubectl"
				args := []string{"apply", "-f", "manifest.yaml"}
				cmd := exec.Command(a, args...)
				err = cmd.Run()
				if err != nil {
					panic(err)
					return err
				}

				// delete the manifest file
				err = os.Remove("manifest.yaml")
				if err != nil {
					fmt.Println("Error deleting manifest file", err)
				}

				return c.JSON(200, "OK")
			})

			e.Start(":7080")
		},
	}

	cmd.Flags().String("kvhost", "http://localhost:9080", "KV host")
	cmd.Flags().String("int", "http://localhost:%s", "Internal node template")
	cmd.Flags().Bool("incp", true, "Increment port by 1 for state query")

	return cmd
}
