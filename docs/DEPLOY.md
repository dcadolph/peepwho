# Deploying WhoToAsk

WhoToAsk is a single binary. The web UI and the Slack bot are long-running; the CLI
and indexing are one-shot. Build the index first, then run a frontend against it.

## Docker

Build the image:

    docker build -t whotoask .

The image serves the web UI by default. Serving beyond localhost requires
WHOTOASK_SERVE_TOKEN: the server refuses to start without it, and every request
must carry the token as an Authorization bearer header or a token query
parameter, which sets a session cookie for the browser. Generate a long random
value, for example `openssl rand -hex 24`.

The image runs as the distroless nonroot user, uid 65532, and its /data
directory belongs to that user, so a new named volume mounted there is
writable. Build the index
into the volume first. This example reads an org chart CSV from the current
directory:

    docker run --rm -v whotoask-data:/data \
      -v "$PWD/people.csv:/in/people.csv:ro" \
      whotoask index --source org-csv --file /in/people.csv --data-dir /data

Then serve from the same volume:

    docker run --rm -p 8765:8765 -v whotoask-data:/data \
      -e WHOTOASK_SERVE_TOKEN=change-me \
      whotoask serve --addr 0.0.0.0:8765 --data-dir /data

A host directory bind-mounted at /data in place of the named volume keeps its
host owner, so it must be writable by uid 65532, for example
`sudo chown 65532:65532 /srv/whotoask`. Otherwise the index cannot be written,
the attest key is kept in memory only, and every feedback vote fails.

Then open http://host:8765/?token=change-me once; the session cookie carries
the rest. The token gates access, not transport: put TLS in front of the
container for anything beyond a trusted network.

For the Slack bot, run the bot subcommand instead and pass the tokens as
environment variables.

## systemd (Slack bot)

Install the binary at /usr/local/bin/whotoask and build the index into
/var/lib/whotoask. Put the tokens in /etc/whotoask/bot.env with mode 0600:

    WHOTOASK_SLACK_TOKEN=xoxb-...
    WHOTOASK_SLACK_APP_TOKEN=xapp-...

Create a dedicated `whotoask` user that owns /var/lib/whotoask, then write
/etc/systemd/system/whotoask-bot.service:

    [Unit]
    Description=whotoask Slack bot
    After=network-online.target
    Wants=network-online.target

    [Service]
    User=whotoask
    EnvironmentFile=/etc/whotoask/bot.env
    ExecStart=/usr/local/bin/whotoask bot --data-dir /var/lib/whotoask
    Restart=on-failure
    NoNewPrivileges=yes
    ProtectSystem=strict
    ProtectHome=yes
    ReadWritePaths=/var/lib/whotoask

    [Install]
    WantedBy=multi-user.target

Then enable it:

    systemctl enable --now whotoask-bot

The unit runs as the dedicated user, restarts on failure, and can write only to
the data directory.

## Organization policy

To lock a managed deployment to strict egress, place a locked policy at
/etc/whotoask/policy.json. See [GETTING_STARTED.md](GETTING_STARTED.md) for the format.
