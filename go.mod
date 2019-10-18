module github.com/pipelined/mixer

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/kr/pretty v0.1.0 // indirect
	github.com/pipelined/signal v0.3.1-0.20191011053735-8dc9da436ce9
	github.com/stretchr/testify v1.4.0
	go.uber.org/goleak v0.10.0
	gopkg.in/check.v1 v1.0.0-20180628173108-788fd7840127 // indirect
)

replace github.com/pipelined/signal => ../signal

go 1.13
