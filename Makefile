.PHONY: all build test check clean setup-db init-demo

DB ?= .fa.db
CARGO ?= cargo
GO ?= go

all: build

build:
	cmake -S . -B build -DCMAKE_BUILD_TYPE=RelWithDebInfo
	cmake --build build --parallel
	$(CARGO) build --release --workspace
	mkdir -p build
	cp target/release/sec build/sec
	$(GO) build -o build/secd ./cmd/secd

test: build
	ctest --test-dir build --output-on-failure
	$(CARGO) test --workspace
	$(GO) test ./cmd/secd

check:
	./check

setup-db: build
	./build/sec init --db "$(DB)"
	@echo "Database ready at $(DB)"

clean:
	rm -rf build target .sec-demo.db raw

init-demo: build
	./build/sec init --db .sec-demo.db
	./build/sec sym --db .sec-demo.db --cik 0000320193 --symbol AAPL --price-usd 200 --adv-usd 5000000000 --investable 1
	./build/sec pos --db .sec-demo.db --cik 0000320193 --quantity-shares 0 --market-value-usd 0 --weight-ratio 0
	@echo "Demo database created at .sec-demo.db"
