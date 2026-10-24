#!/bin/sh
# Pterodactyl "yolks" convention: Wings passes the egg's startup command in
# $STARTUP with {{VAR}} placeholders. Substitute them from the environment and run it.
# Without STARTUP (plain docker run) the gateway starts directly.
cd /home/container || exit 1

if [ -z "${STARTUP}" ]; then
    exec /usr/local/bin/peb-gateway
fi

PARSED=$(echo "${STARTUP}" | sed -e 's/{{/${/g' -e 's/}}/}/g' | eval echo "$(cat -)")
echo ":/home/container$ ${PARSED}"
# shellcheck disable=SC2086
exec env ${PARSED}
