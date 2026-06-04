module instant.dev/common

go 1.25.0

toolchain go1.25.11

require (
	github.com/golang-jwt/jwt/v4 v4.5.2
	github.com/google/uuid v1.6.0
	github.com/nats-io/jwt/v2 v2.8.1
	github.com/nats-io/nkeys v0.4.15
	github.com/stretchr/testify v1.11.1
	gopkg.in/yaml.v3 v3.0.1
	instant.dev/proto v0.0.0
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	golang.org/x/crypto v0.52.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)

replace instant.dev/proto => ../proto
