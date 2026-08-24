module github.com/opsi-dev/opsi/cloud

go 1.26.4

require (
	github.com/jackc/pgx/v5 v5.10.0
	github.com/opsi-dev/opsi/contracts/go v0.0.0
	golang.org/x/crypto v0.48.0
	gopkg.in/yaml.v3 v3.0.1
)

replace github.com/opsi-dev/opsi/contracts/go => ../contracts/go

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/net v0.49.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250804133106-a7a43d27e69b // indirect
	google.golang.org/grpc v1.76.0 // indirect
	google.golang.org/protobuf v1.36.6 // indirect
)
