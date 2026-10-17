// sandbox/gatewayd is a SEPARATE MODULE: the sandbox gateway server and its
// agentkit-gateway command, a network service kept out of the root and
// sandbox modules so they listen on no socket.
module github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd

go 1.27

require (
	github.com/agent-fox-dev/agentkit-go v0.0.0
	github.com/agent-fox-dev/agentkit-go/sandbox v0.0.0
	google.golang.org/grpc v1.64.1
)

require (
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/bmatcuk/doublestar/v4 v4.10.2 // indirect
	github.com/coreos/go-systemd v0.0.0-20191104093116-d3cd4ed1dbcf // indirect
	github.com/gokrazy/rsync v0.3.8 // indirect
	github.com/google/renameio/v2 v2.0.2 // indirect
	github.com/google/shlex v0.0.0-20191202100458-e7afc7fbc510 // indirect
	github.com/landlock-lsm/go-landlock v0.0.0-20250303204525-1544bccde3a3 // indirect
	github.com/mattn/go-pointer v0.0.1 // indirect
	github.com/mmcloughlin/md4 v0.1.2 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	github.com/tree-sitter-grammars/tree-sitter-kotlin v1.1.0 // indirect
	github.com/tree-sitter-grammars/tree-sitter-lua v0.5.0 // indirect
	github.com/tree-sitter/go-tree-sitter v0.25.0 // indirect
	github.com/tree-sitter/tree-sitter-bash v0.25.1 // indirect
	github.com/tree-sitter/tree-sitter-c v0.24.2 // indirect
	github.com/tree-sitter/tree-sitter-c-sharp v0.23.5 // indirect
	github.com/tree-sitter/tree-sitter-cpp v0.23.4 // indirect
	github.com/tree-sitter/tree-sitter-java v0.23.5 // indirect
	github.com/tree-sitter/tree-sitter-javascript v0.25.0 // indirect
	github.com/tree-sitter/tree-sitter-php v0.25.1 // indirect
	github.com/tree-sitter/tree-sitter-python v0.25.0 // indirect
	github.com/tree-sitter/tree-sitter-ruby v0.23.1 // indirect
	github.com/tree-sitter/tree-sitter-rust v0.24.2 // indirect
	github.com/tree-sitter/tree-sitter-scala v0.26.2 // indirect
	github.com/tree-sitter/tree-sitter-typescript v0.23.2 // indirect
	golang.org/x/crypto v0.46.0 // indirect
	golang.org/x/net v0.47.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/term v0.41.0 // indirect
	golang.org/x/text v0.32.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240722135656-d784300faade // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	kernel.org/pub/linux/libs/security/libcap/psx v1.2.70 // indirect
)

replace (
	github.com/agent-fox-dev/agentkit-go => ../../
	github.com/agent-fox-dev/agentkit-go/sandbox => ../
)
