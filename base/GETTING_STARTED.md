# Go Generic Service

A plain Go service with a declared, private HTTP endpoint.

## Structure

- `code/main.go` serves `/health` at the address Codefly assigns to `http`.
- `code/go.mod` declares the service's dependencies.
- `service.codefly.yaml` declares the endpoint and development settings.

## Validation and running

From `code/`, build with `go build ./...` and test with `go test ./...`.
Follow the handbook's [Codefly run instructions](https://github.com/obin-ai/handbook/blob/main/getting-started/codefly.md#how-do-i-run-it)
to run the service with its resolved identity and endpoint. The generated server
refuses startup when it cannot resolve that address.

Replace the default handler with your service's behavior.
