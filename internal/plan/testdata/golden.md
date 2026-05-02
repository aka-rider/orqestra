# Orqestra Plan

## SchemaVersion

1

## Goal

Add tests for the user service

## Context

The user service is tested using Go's testing package.

## Steps

1. Create user_service_test.go
2. Write unit tests for CreateUser
3. Write unit tests for GetUser

## Acceptance

1. All tests pass with go test ./...
2. Coverage above 80%

## Constraints

1. Do not modify production code

## Risks

1. Tests may be flaky if database is not mocked

## ValidationCommands

```
go test ./...
go vet ./...
```

## ExpectedArtifacts

1. internal/user/user_service_test.go
