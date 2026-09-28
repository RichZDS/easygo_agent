module easygo-agent/services/ai-gateway

go 1.25.0

require easygo-agent/rpc v0.0.0

require (
	go.etcd.io/bbolt v1.4.3 // indirect
	golang.org/x/sys v0.29.0 // indirect
)

replace easygo-agent/rpc => ../../packages/rpc-go
