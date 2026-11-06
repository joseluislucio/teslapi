# teslapi — tareas de desarrollo.
# El portal va embebido en el binario: compila siempre la web antes que el daemon.

.PHONY: web build test dev deploy provision e2e i18n

web:
	cd web && npm run build --silent

build: web
	cd daemon && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o ../build/teslapid ./cmd/teslapid

test: i18n
	cd daemon && go vet ./... && go vet -tags dev ./... && GOOS=linux GOARCH=arm64 go vet ./... && go test -race -count=1 ./...

# Daemon en el Mac con controladores falsos y los clips de .devdata (portal en http://localhost:8080).
dev: web
	cd daemon && go build -tags dev -o ../build/teslapid-dev ./cmd/teslapid
	./build/teslapid-dev -dev "$(CURDIR)/.devdata" -listen 127.0.0.1:8080

# Con `make dev` corriendo: reproducción sincronizada del visor en Chrome headless.
#   make e2e EVENT=<carpeta de .devdata/TeslaCam/SavedClips>
e2e:
	@test -n "$(EVENT)" || { echo "uso: make e2e EVENT=<carpeta de .devdata/TeslaCam/SavedClips>"; exit 1; }
	python3 test/e2e/viewer_sync.py http://localhost:8080 "TeslaCam/SavedClips/$(EVENT)" "$(EVENT)"

deploy:
	scripts/deploy.sh

provision:
	scripts/deploy.sh provision

# Comprueba que todos los idiomas tienen todas las claves y que no hay claves sin definir.
i18n:
	node web/scripts/check-i18n.mjs
