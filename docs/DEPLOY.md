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

Mount a data directory that holds a prebuilt index, or run the index command
against the same volume first:

    docker run --rm -p 8765:8765 -v whotoask-data:/data \
      -e WHOTOASK_SERVE_TOKEN=change-me \
      whotoask serve --addr 0.0.0.0:8765 --data-dir /data

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
