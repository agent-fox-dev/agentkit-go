// examples/harness is its own module because it imports the sandbox module,
// which the root module does not require.
module github.com/agent-fox-dev/agentkit-go/examples/harness

go 1.27

require (
	github.com/agent-fox-dev/agentkit-go v0.0.0
	github.com/agent-fox-dev/agentkit-go/mcpserve v0.0.0
	github.com/agent-fox-dev/agentkit-go/sandbox v0.0.0
)

require (
	cloud.google.com/go/auth v0.7.2 // indirect
	cloud.google.com/go/auth/oauth2adapt v0.2.3 // indirect
	cloud.google.com/go/compute/metadata v0.5.0 // indirect
	github.com/BurntSushi/toml v1.6.0 // indirect
	github.com/anthropics/anthropic-sdk-go v1.79.1 // indirect
	github.com/aws/aws-sdk-go-v2 v1.38.0 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.6.3 // indirect
	github.com/aws/aws-sdk-go-v2/config v1.27.27 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.17.27 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.16.11 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.3.15 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.6.15 // indirect
	github.com/aws/aws-sdk-go-v2/internal/ini v1.8.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.11.3 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.11.17 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.22.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.26.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.30.3 // indirect
	github.com/aws/smithy-go v1.22.5 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/bmatcuk/doublestar/v4 v4.10.2 // indirect
	github.com/buger/jsonparser v1.1.2 // indirect
	github.com/coreos/go-systemd v0.0.0-20191104093116-d3cd4ed1dbcf // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/gokrazy/rsync v0.3.8 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/renameio/v2 v2.0.2 // indirect
	github.com/google/s2a-go v0.1.7 // indirect
	github.com/google/shlex v0.0.0-20191202100458-e7afc7fbc510 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.2 // indirect
	github.com/invopop/jsonschema v0.14.0 // indirect
	github.com/landlock-lsm/go-landlock v0.0.0-20250303204525-1544bccde3a3 // indirect
	github.com/mattn/go-pointer v0.0.1 // indirect
	github.com/mmcloughlin/md4 v0.1.2 // indirect
	github.com/modelcontextprotocol/go-sdk v1.8.0 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	github.com/pelletier/go-toml/v2 v2.4.3 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/standard-webhooks/standard-webhooks/libraries v0.0.1 // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
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
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.opencensus.io v0.24.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.49.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.49.0 // indirect
	go.opentelemetry.io/otel v1.24.0 // indirect
	go.opentelemetry.io/otel/metric v1.24.0 // indirect
	go.opentelemetry.io/otel/trace v1.24.0 // indirect
	go.starlark.net v0.0.0-20261005163335-bcb1a1a55bf9 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.2 // indirect
	golang.org/x/crypto v0.46.0 // indirect
	golang.org/x/net v0.47.0 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.32.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	google.golang.org/api v0.189.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240722135656-d784300faade // indirect
	google.golang.org/grpc v1.64.1 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	kernel.org/pub/linux/libs/security/libcap/psx v1.2.70 // indirect
)

replace (
	github.com/agent-fox-dev/agentkit-go => ../..
	github.com/agent-fox-dev/agentkit-go/mcpserve => ../../mcpserve
	github.com/agent-fox-dev/agentkit-go/sandbox => ../../sandbox
)
