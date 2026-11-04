# Deploying peepwho

peepwho is a single binary. The web UI and the Slack bot are long-running; the CLI
and indexing are one-shot. Build the index first, then run a frontend against it.

## Docker

Build the image:

    docker build -t peepwho .

The image serves the web UI by default. Serving beyond localhost requires
PEEPWHO_SERVE_TOKEN: the server refuses to start without it, and every request
must carry the token as an Authorization bearer header or a token query
parameter, which sets a session cookie for the browser. Generate a long random
value, for example `openssl rand -hex 24`.

Mount a data directory that holds a prebuilt index, or run the index command
against the same volume first:

    docker run --rm -p 8765:8765 -v peepwho-data:/data \
      -e PEEPWHO_SERVE_TOKEN=change-me \
      peepwho serve --addr 0.0.0.0:8765 --data-dir /data

Then open http://host:8765/?token=change-me once; the session cookie carries
the rest. The token gates access, not transport: put TLS in front of the
container for anything beyond a trusted network.

For the Slack bot, run the bot subcommand instead and pass the tokens as
environment variables.

## systemd (Slack bot)

Install the binary at /usr/local/bin/peepwho and build the index into
/var/lib/peepwho. Put the tokens in /etc/peepwho/bot.env with mode 0600:

    PEEPWHO_SLACK_TOKEN=xoxb-...
    PEEPWHO_SLACK_APP_TOKEN=xapp-...

Create a dedicated `peepwho` user that owns /var/lib/peepwho, then write
/etc/systemd/system/peepwho-bot.service:

    [Unit]
    Description=peepwho Slack bot
    After=network-online.target
    Wants=network-online.target

    [Service]
    User=peepwho
    EnvironmentFile=/etc/peepwho/bot.env
    ExecStart=/usr/local/bin/peepwho bot --data-dir /var/lib/peepwho
    Restart=on-failure
    NoNewPrivileges=yes
    ProtectSystem=strict
    ProtectHome=yes
    ReadWritePaths=/var/lib/peepwho

    [Install]
    WantedBy=multi-user.target

Then enable it:

    systemctl enable --now peepwho-bot

The unit runs as the dedicated user, restarts on failure, and can write only to
the data directory.

## Organization policy

To lock a managed deployment to strict egress, place a locked policy at
/etc/peepwho/policy.json. See [GETTING_STARTED.md](GETTING_STARTED.md) for the format.
