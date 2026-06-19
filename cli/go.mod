module github.com/opsi-dev/opsi/cli

go 1.26.4

require (
	github.com/99designs/keyring v1.2.2
	github.com/opsi-dev/opsi/contracts/go v0.0.0
	github.com/spf13/cobra v1.10.1
	google.golang.org/grpc v1.76.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/99designs/go-keychain v0.0.0-20191008050251-8e49817e8af4 // indirect
	github.com/danieljoos/wincred v1.1.2 // indirect
	github.com/dvsekhvalnov/jose2go v1.5.0 // indirect
	github.com/godbus/dbus v0.0.0-20190726142602-4481cbc300e2 // indirect
	github.com/gsterjov/go-libsecret v0.0.0-20161001094733-a6f4afe4910c // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/mtibben/percent v0.2.1 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	golang.org/x/net v0.42.0 // indirect
	golang.org/x/sys v0.34.0 // indirect
	golang.org/x/term v0.33.0 // indirect
	golang.org/x/text v0.27.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250804133106-a7a43d27e69b // indirect
	google.golang.org/protobuf v1.36.6 // indirect
)

replace github.com/opsi-dev/opsi/contracts/go => ../contracts/go
