.PHONY: all build test check clean init-demo

all: build

build:
	cmake -S . -B build -DCMAKE_BUILD_TYPE=RelWithDebInfo
	cmake --build build --parallel

test: build
	ctest --test-dir build --output-on-failure

check:
	./ci/check

clean:
	rm -rf build .sec-demo.db raw

init-demo: build
	bin/init --db .sec-demo.db
	bin/sym --db .sec-demo.db --cik 0000320193 --symbol AAPL --price-usd 200 --adv-usd 5000000000 --investable 1
	bin/pos --db .sec-demo.db --cik 0000320193 --quantity-shares 0 --market-value-usd 0 --weight-ratio 0
	@echo "Demo database created at .sec-demo.db"
