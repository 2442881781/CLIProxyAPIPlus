module cpa-plugin-museai

go 1.26.0

require (
	github.com/gorilla/websocket v1.5.3
	github.com/router-for-me/CLIProxyAPI/v8 v8.0.0
	gopkg.in/yaml.v3 v3.0.1
)

require golang.org/x/net v0.57.0 // indirect

replace github.com/router-for-me/CLIProxyAPI/v8 => ../..
