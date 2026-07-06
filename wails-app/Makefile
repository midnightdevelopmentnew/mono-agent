APP_BUNDLE = build/bin/MonoAgent.app
APP_MACOS  = $(APP_BUNDLE)/Contents/MacOS

.PHONY: build run dev clean

# Development mode: hot-reload frontend + browser DevTools
dev:
	~/go/bin/wails dev

build:
	@echo "→ Building monoagentcli..."
	cd .. && go build -o wails-app/build/bin/monoagentcli ./cmd/monoagentcli/
	@echo "→ Building Wails app..."
	~/go/bin/wails build
	@echo "→ Bundling monoagentcli into app..."
	cp build/bin/monoagentcli "$(APP_MACOS)/monoagentcli"
	@echo "✓ Done: $(APP_BUNDLE)"

run: build
	open "$(APP_BUNDLE)"

clean:
	rm -rf build/bin
