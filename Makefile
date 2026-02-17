.PHONY: build install clean test service-install service-start service-stop

build:
	go build -o cortex ./cmd/cortex/

install: build
	cp cortex ~/.local/bin/

clean:
	rm -f cortex

test:
	go test ./...

test-race:
	go test -race ./...

service-install:
	mkdir -p ~/.config/systemd/user/
	cp cortex.service ~/.config/systemd/user/
	systemctl --user daemon-reload

service-start:
	systemctl --user enable --now cortex.service

service-stop:
	systemctl --user stop cortex.service
	systemctl --user disable cortex.service
