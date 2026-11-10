# telegram-channel-index

Telegram bot that keeps a list of channels under a fixed set of tags.

People search and vote with the buttons on a result. The service polls Telegram, so it opens no port.

This is its own program. Other programs talk to the running bot the same way a person does. It is not part of jellyfin-telegram.

## Run

```
login    sign the checker account in once
serve    run the bot and the checker
```

Set `BOT_TOKEN`, `ADMIN_IDS` (comma separated Telegram user ids), `TG_API_ID` and `TG_API_HASH`. The session from `login` is stored in `DATA_DIR` (default `data`).

`OPEN=false` answers only admins and people added with `/allow`. `PRIVATE_LINKS=true` lets `/add` take a private invite link. The checker never joins a channel.

```
docker compose up -d
```

The image is built elsewhere and pulled. Do not build it on the server.

## Commands

```
/tags
/search <tag> [page]
/add @channel <tag> [tag...]
/tag @channel <tag>
/info @channel
```

Admins can also `/hide`, `/untag`, `/allow`, `/deny` and `/purge`.
