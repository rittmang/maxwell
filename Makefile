.PHONY: test fmt

test:
	go test ./...

fmt:
	gofmt -w $$(rg --files -g '*.go')
