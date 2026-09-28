module easygo-agent/services/workshop

go 1.25.0

require easygo-agent/rpc v0.0.0

replace easygo-agent/rpc => ../../packages/rpc-go

require (
	github.com/google/uuid v1.6.0
	go.etcd.io/bbolt v1.4.3
)

require golang.org/x/sys v0.29.0 // indirect
