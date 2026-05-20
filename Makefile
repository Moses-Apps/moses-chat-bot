.PHONY: build test lint clean backend-build backend-test backend-vet frontend-install frontend-build frontend-test frontend-lint ci-test pre-commit

build: backend-build frontend-build

test: backend-test frontend-test

lint: backend-vet frontend-lint

clean:
	rm -rf backend/server
	rm -rf frontend/dist frontend/node_modules

backend-build:
	cd backend && go build ./...

backend-test:
	cd backend && go test ./...

backend-vet:
	cd backend && go vet ./...

frontend-install:
	cd frontend && npm install

frontend-build: frontend-install
	cd frontend && npm run build

frontend-test: frontend-install
	cd frontend && npm test -- --run

frontend-lint: frontend-install
	cd frontend && npm run lint

# Mirrors the backend + frontend CI jobs so devs can reproduce a CI run locally.
# staticcheck is installed into GOBIN if missing.
ci-test:
	cd backend && go mod download
	cd backend && go vet ./...
	@command -v staticcheck >/dev/null 2>&1 || go install honnef.co/go/tools/cmd/staticcheck@latest
	cd backend && staticcheck ./...
	cd backend && go test -race -cover ./...
	cd backend && go build ./...
	cd frontend && npm ci
	cd frontend && npm run lint
	cd frontend && npm test -- --run
	cd frontend && npm run build

# Run pre-commit hooks across the whole tree. Prints install hint if absent.
pre-commit:
	@if command -v pre-commit >/dev/null 2>&1; then \
		pre-commit run --all-files; \
	else \
		echo "pre-commit not installed. Install with:"; \
		echo "  pip install pre-commit && pre-commit install"; \
		exit 1; \
	fi
