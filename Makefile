.PHONY: all build test check clean setup-db init-demo

DB ?= .fa.db

all: build

build:
	cmake -S . -B build -DCMAKE_BUILD_TYPE=RelWithDebInfo
	cmake --build build --parallel
	go build -o build/sec ./cli

test: build
	ctest --test-dir build --output-on-failure

check:
	./check

setup-db:
	./sec init --db "$(DB)"
	@echo "Database ready at $(DB)"

clean:
	rm -rf build .sec-demo.db raw

init-demo: build
	./sec init --db .sec-demo.db
	./sec sym --db .sec-demo.db --cik 0000320193 --symbol AAPL --price-usd 200 --adv-usd 5000000000 --investable 1
	./sec pos --db .sec-demo.db --cik 0000320193 --quantity-shares 0 --market-value-usd 0 --weight-ratio 0
	@echo "Demo database created at .sec-demo.db"
