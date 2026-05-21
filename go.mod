module instant.dev/common

go 1.25.0

toolchain go1.25.10

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
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace instant.dev/proto => ../proto
