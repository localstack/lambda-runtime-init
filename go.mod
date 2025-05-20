module github.com/localstack/lambda-runtime-init

go 1.24

require (
	github.com/aws/aws-sdk-go v1.44.298
	github.com/aws/aws-xray-daemon v0.0.0-20250212175715-5defe1b8d61b
	github.com/cihub/seelog v0.0.0-20170130134532-f561c5e57575
	github.com/fsnotify/fsnotify v1.6.0
	github.com/go-chi/chi v1.5.5
	github.com/shirou/gopsutil v2.19.10+incompatible
	github.com/sirupsen/logrus v1.9.3
	go.amzn.com v0.0.0-00010101000000-000000000000
	golang.org/x/sys v0.31.0
)

require (
	github.com/StackExchange/wmi v0.0.0-20190523213315-cbe66965904d // indirect
	github.com/go-ole/go-ole v1.2.4 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	golang.org/x/net v0.38.0 // indirect
	golang.org/x/text v0.23.0 // indirect
	gopkg.in/yaml.v2 v2.2.8 // indirect
)

replace go.amzn.com => github.com/aws/aws-lambda-runtime-interface-emulator v0.0.0-20250423173140-3a0772eae98d
