# Free-Extension "PEB Backups". Regeln stehen in ../tools/extension.mk (make setup, install, check, pack).
EXT_ID := peb-backups

include ../tools/extension.mk

# Das mitgelieferte Egg ist eine Kopie von ../egg/egg-peb-gateway.json.
# `make egg-sync` kopiert, `make check` bricht ab, wenn beide auseinanderlaufen.
EGG_SRC := ../egg/egg-peb-gateway.json
EGG_DST := resources/egg/egg-peb-gateway.json

.PHONY: egg-sync egg-check
egg-sync:
	mkdir -p $(dir $(EGG_DST))
	cp $(EGG_SRC) $(EGG_DST)

egg-check:
	@cmp -s $(EGG_SRC) $(EGG_DST) || { echo "$(EGG_DST) weicht von $(EGG_SRC) ab: make egg-sync"; exit 1; }

check: egg-check
