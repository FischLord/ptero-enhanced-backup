# Gemeinsame Build-, Test- und Paketierungsregeln fuer die PEB-Extensions (alles in Docker).
# Auf dem Host sind weder Node noch PHP noetig, nur Docker.
#
# Aufruf aus dem Makefile einer Extension, vor dem include:
#   EXT_ID := peb-backups                      (Pflicht)
#   DEPS   := peb-backups=$(abspath ../extension)   (optional: andere Extensions, die beim
#                                              Test/Doctor unter /panel/extensions/<id> liegen muessen)
#   include ../tools/extension.mk
#
#   make setup      einmalig: Panel-Quellen (Tag v2.0.0-beta.2) holen, SDK-Testruntime bauen
#   make install    npm-Abhaengigkeiten der Extension
#   make check      typecheck + Tests (JS und PHP)
#   make pack       build + doctor + .pteroext nach build/
#
# Das Panel-Checkout liegt standardmaessig unter ~/.cache/peb-ext/panel-src (nicht im Repo).
# Hinweis: Docker Desktop teilt nur bestimmte Pfade mit Containern, /tmp gehoert meist nicht dazu.

PANEL_TAG   ?= v2.0.0-beta.2
PANEL_SRC   ?= $(HOME)/.cache/peb-ext/panel-src
VERSION     := $(shell sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' extension.json | head -1)
PACKAGE     := build/$(EXT_ID)-$(VERSION).pteroext
UIDGID      := $(shell id -u):$(shell id -g)
TOOLS_DIR   := $(dir $(lastword $(MAKEFILE_LIST)))

NODE_IMAGE  ?= node:22
PHP_IMAGE   ?= peb-ext-php

DEP_MOUNTS  := $(foreach d,$(DEPS),-v $(word 2,$(subst =, ,$(d))):/panel/extensions/$(word 1,$(subst =, ,$(d))))
MOUNTS      := -v $(PANEL_SRC):/panel -v $(CURDIR):/panel/extensions/$(EXT_ID) $(DEP_MOUNTS)
NODE        := docker run --rm -u $(UIDGID) -e HOME=/tmp -e npm_config_cache=/tmp/npm $(MOUNTS) -w /panel/extensions/$(EXT_ID) $(NODE_IMAGE)
NODE_PANEL  := docker run --rm -u $(UIDGID) -e HOME=/tmp -e npm_config_cache=/tmp/npm -v $(PANEL_SRC):/panel -w /panel $(NODE_IMAGE)
PHP         := docker run --rm -u $(UIDGID) -e HOME=/tmp -e COMPOSER_HOME=/tmp/composer -e PANEL_ROOT=/panel $(MOUNTS) -w /panel $(PHP_IMAGE)

.PHONY: setup php-image install lang typecheck test test-js test-php check build doctor pack clean

setup: php-image
	test -d $(PANEL_SRC)/.git || git clone --depth 1 --branch $(PANEL_TAG) https://github.com/pterodactyl/panel.git $(PANEL_SRC)
	mkdir -p $(PANEL_SRC)/extensions/$(EXT_ID)
	$(NODE_PANEL) sh -c "npm ci --no-audit --no-fund && npm run sdk:testing"
	docker run --rm -u $(UIDGID) -e HOME=/tmp -e COMPOSER_HOME=/tmp/composer -v $(PANEL_SRC):/panel -w /panel $(PHP_IMAGE) \
	  sh -c "composer install --no-interaction --no-progress; cp -n .env.example .env; php artisan key:generate --force"

php-image:
	docker build -q -t $(PHP_IMAGE) -f $(TOOLS_DIR)Dockerfile.php $(TOOLS_DIR)

# npm 10 (in node:22) scheitert am Peer-Dependency-Baum des SDK ("edgesOut"-Fehler), npm 11 nicht.
install:
	$(NODE) npx -y npm@11 install --no-audit --no-fund

lang:
	$(NODE) npm run lang

typecheck:
	$(NODE) npm run typecheck

test-js:
	$(NODE) npm test

test-php:
	$(PHP) vendor/bin/pest --bootstrap extensions/$(EXT_ID)/tests/bootstrap.php extensions/$(EXT_ID)/tests/Unit

test: test-js test-php

check: typecheck test

build:
	$(NODE) npm run build

# Der Doctor prueft requires.extensions gegen die Tabelle `extensions`. Fuer Extensions mit DEPS legt er
# deshalb eine Wegwerf-SQLite-Datenbank im Container an, in der die Abhaengigkeiten als aktiviert stehen.
DEP_IDS     := $(foreach d,$(DEPS),$(word 1,$(subst =, ,$(d))))
DOCTOR_DB   := $(if $(DEPS),sh -c 'rm -f /tmp/peb-doctor.sqlite && php -r "\$$p=new PDO(\"sqlite:/tmp/peb-doctor.sqlite\");\$$p->exec(\"CREATE TABLE extensions (id integer primary key, identifier text, version text, enabled integer, error text, error_phase text, created_at text, updated_at text)\");foreach(explode(\",\",\"$(subst $(space),$(comma),$(DEP_IDS))\") as \$$i)\$$p->exec(\"INSERT INTO extensions (identifier,version,enabled) VALUES (\x27\$$i\x27,\x270\x27,1)\");" && DB_CONNECTION=sqlite DB_DATABASE=/tmp/peb-doctor.sqlite php artisan p:extension:doctor extensions/$(EXT_ID)',php artisan p:extension:doctor extensions/$(EXT_ID))
space :=
space +=
comma := ,

doctor: build
	$(PHP) $(DOCTOR_DB)

pack: check doctor
	mkdir -p build
	$(PHP) php artisan p:extension:pack extensions/$(EXT_ID) --output=extensions/$(EXT_ID)/$(PACKAGE) --force
	@ls -l $(PACKAGE)

clean:
	rm -rf dist build resources/lang node_modules
